package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EntryPointResolver coordinates safe scanning, language detection, and language adapter
// dispatching to resolve entry points and execution modes for arbitrary repositories.
type EntryPointResolver struct {
	scanner  *RepositoryScanner
	detector *LanguageDetector
	registry *AdapterRegistry
	compiler *NativeCompiler
}

// NewEntryPointResolver creates a fully initialized EntryPointResolver.
func NewEntryPointResolver() *EntryPointResolver {
	return &EntryPointResolver{
		scanner:  NewRepositoryScanner(),
		detector: NewLanguageDetector(),
		registry: NewAdapterRegistry(),
		compiler: NewNativeCompiler(),
	}
}

// Registry returns the active adapter registry.
func (r *EntryPointResolver) Registry() *AdapterRegistry {
	return r.registry
}

// Compiler returns the native compiler engine.
func (r *EntryPointResolver) Compiler() *NativeCompiler {
	return r.compiler
}

// ResolveEntryPoints analyzes the repository using modular language adapters to identify
// runnable entry points, execution mode, and compilation commands without relying on hardcoded filenames.
func (r *EntryPointResolver) ResolveEntryPoints(
	ctx context.Context,
	repoDir string,
	analysis *RepositoryAnalysis,
	userCfg *UserBuildConfig,
) (*EntryPointResolution, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("entry point resolver: repoDir cannot be empty")
	}

	cleanRoot, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("entry point resolver: resolving path %q: %w", repoDir, err)
	}

	res := &EntryPointResolution{
		Candidates:    make([]EntryPointCandidate, 0),
		ExecutionMode: ExecutionModeUnsupported,
		Confidence:    ConfidenceLow,
		Warnings:      make([]string, 0),
	}

	// 1. Precedence Tier 1: Explicit User Command
	if userCfg != nil && strings.TrimSpace(userCfg.StartCmd) != "" {
		userCmd := strings.TrimSpace(userCfg.StartCmd)
		res.SelectedCommand = userCmd
		res.CommandSource = "USER_EXPLICIT"
		res.Confidence = ConfidenceHigh
		res.ExecutionMode = ExecutionModeApplication

		// If user command looks like a direct script or local binary execution
		if strings.HasPrefix(userCmd, "python ") || strings.HasPrefix(userCmd, "node ") ||
			strings.HasPrefix(userCmd, "go run ") || strings.HasPrefix(userCmd, "./") {
			res.ExecutionMode = ExecutionModeScript
		}

		res.PrimaryCandidate = &EntryPointCandidate{
			SuggestedCommand: userCmd,
			Confidence:       ConfidenceHigh,
			ExecutionMode:    res.ExecutionMode,
			Reason:           "explicit user start command",
			Score:            1000,
		}
		return res, nil
	}

	// 2. Precedence Tier 2: Explicit Project Configuration
	if rpStart := readRailpackJSONStart(cleanRoot); rpStart != "" {
		res.SelectedCommand = rpStart
		res.CommandSource = "PROJECT_CONFIG"
		res.Confidence = ConfidenceHigh
		res.ExecutionMode = ExecutionModeApplication
		res.PrimaryCandidate = &EntryPointCandidate{
			SuggestedCommand: rpStart,
			Confidence:       ConfidenceHigh,
			ExecutionMode:    ExecutionModeApplication,
			Reason:           "configured in railpack.json",
			Score:            500,
		}
		return res, nil
	}

	if procStart := readProcfileWeb(cleanRoot); procStart != "" {
		res.SelectedCommand = procStart
		res.CommandSource = "PROJECT_CONFIG"
		res.Confidence = ConfidenceHigh
		res.ExecutionMode = ExecutionModeApplication
		res.PrimaryCandidate = &EntryPointCandidate{
			SuggestedCommand: procStart,
			Confidence:       ConfidenceHigh,
			ExecutionMode:    ExecutionModeApplication,
			Reason:           "configured in Procfile web process",
			Score:            450,
		}
		return res, nil
	}

	// 3. Scan repository safely with bounds and symlink checks
	scannedRepo, err := r.scanner.Scan(ctx, cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("entry point resolver: scanning repository: %w", err)
	}
	res.Warnings = append(res.Warnings, scannedRepo.Warnings...)

	// 4. Detect languages with strict evidence precedence (separate from entry-point resolution)
	detection := r.detector.DetectLanguages(ctx, scannedRepo, userCfg)
	res.DetectedLanguages = detection.DetectedLanguages

	if detection.IsMixedLanguage {
		var langs []string
		for _, dl := range detection.DetectedLanguages {
			langs = append(langs, dl.Language)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("mixed-language repository detected (%s)", strings.Join(langs, ", ")))
	}

	// 5. Evaluate candidate source files through modular LanguageAdapters
	for _, sf := range scannedRepo.Files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		adapter, hasAdapter := r.registry.GetByExtension(sf.Extension)
		if !hasAdapter {
			continue
		}

		// Read bounded snippet without executing code
		snippet, err := r.scanner.ReadSnippet(sf)
		if err != nil {
			continue
		}

		isSingleSource := (len(scannedRepo.FilesByExt[sf.Extension]) == 1)
		candidate, err := adapter.InspectFile(ctx, sf, snippet, isSingleSource)
		if err != nil || candidate == nil {
			continue
		}

		// Discard files with negative viability score (e.g. C/C++ helper files without main)
		if candidate.Score <= 0 {
			continue
		}

		if adapter.IsCompiled() {
			candidate.RequiresCompile = true
			candidate.BuildCommand = adapter.BuildCommand(*candidate, "build")
		}

		res.Candidates = append(res.Candidates, *candidate)
	}

	// 6. Check project manifests for Node.js and Java
	if hasPkgJSON(scannedRepo) {
		r.checkNodePackageJSON(cleanRoot, res)
	}
	r.checkJavaManifests(scannedRepo, res)

	if len(res.Candidates) == 0 {
		res.ExecutionMode = ExecutionModeUnsupported
		res.Confidence = ConfidenceLow
		return res, nil
	}

	// 7. Sort candidates by Score descending
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		return res.Candidates[i].Score > res.Candidates[j].Score
	})

	topCandidate := res.Candidates[0]

	// 8. Ambiguity Resolution: Check if multiple equally plausible candidates exist
	if len(res.Candidates) > 1 {
		secondCandidate := res.Candidates[1]
		scoreDiff := topCandidate.Score - secondCandidate.Score

		// If score difference is small (<= 15) and neither is a dominant conventional filename or framework
		if scoreDiff <= 15 && topCandidate.Score < 120 {
			res.IsAmbiguous = true
			res.ExecutionMode = ExecutionModeAmbiguous
			res.CommandSource = "AMBIGUOUS"
			res.Confidence = ConfidenceLow
			res.SelectedCommand = ""
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("multiple equally plausible entry points found (%s, %s); select one to execute",
					topCandidate.RelativePath, secondCandidate.RelativePath))
			return res, nil
		}
	}

	// 9. Unambiguous Primary Candidate Selected
	res.PrimaryCandidate = &topCandidate
	res.SelectedCommand = topCandidate.SuggestedCommand
	res.BuildCommand = topCandidate.BuildCommand
	res.ExecutionMode = topCandidate.ExecutionMode
	res.Confidence = topCandidate.Confidence

	// Assign CommandSource
	if topCandidate.RelativePath == "package.json" || strings.HasSuffix(topCandidate.RelativePath, "pom.xml") || strings.HasSuffix(topCandidate.RelativePath, "build.gradle") {
		res.CommandSource = "PROJECT_CONFIG"
		return res, nil
	}

	ext := filepath.Ext(topCandidate.RelativePath)
	isOnlyFileOfExt := len(scannedRepo.FilesByExt[ext]) == 1

	if topCandidate.RequiresCompile && isOnlyFileOfExt {
		res.CommandSource = "SINGLE_FILE_INFERENCE"
	} else if topCandidate.RequiresCompile {
		res.CommandSource = "NATIVE_COMPILATION"
	} else if isOnlyFileOfExt && len(res.Candidates) == 1 {
		res.CommandSource = "SINGLE_FILE_INFERENCE"
	} else if topCandidate.Framework != "" {
		res.CommandSource = "FRAMEWORK_CONVENTION"
	} else if topCandidate.Score >= 80 {
		res.CommandSource = "CONVENTIONAL_FILENAME"
	} else {
		res.CommandSource = "SINGLE_FILE_INFERENCE"
	}

	return res, nil
}

func hasPkgJSON(repo *ScannedRepository) bool {
	for _, m := range repo.Manifests {
		if strings.HasSuffix(m, "package.json") {
			return true
		}
	}
	return false
}

func (r *EntryPointResolver) checkNodePackageJSON(repoDir string, res *EntryPointResolution) {
	pkgJSONPath := filepath.Join(repoDir, "package.json")
	data, err := os.ReadFile(pkgJSONPath)
	if err != nil {
		return
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
		Main    string            `json:"main"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}

	if startCmd, ok := pkg.Scripts["start"]; ok && strings.TrimSpace(startCmd) != "" {
		res.Candidates = append(res.Candidates, EntryPointCandidate{
			RelativePath:     "package.json",
			Language:         "nodejs",
			ExecutionMode:    ExecutionModeApplication,
			Confidence:       ConfidenceHigh,
			SuggestedCommand: "npm start",
			Score:            300,
			Reason:           "package.json start script",
		})
	}
}

func (r *EntryPointResolver) checkJavaManifests(repo *ScannedRepository, res *EntryPointResolution) {
	for _, m := range repo.Manifests {
		base := strings.ToLower(filepath.Base(m))
		if base == "pom.xml" {
			res.Candidates = append(res.Candidates, EntryPointCandidate{
				RelativePath:     m,
				Language:         "java",
				ExecutionMode:    ExecutionModeApplication,
				Confidence:       ConfidenceHigh,
				SuggestedCommand: "mvn spring-boot:run",
				Score:            250,
				Reason:           "maven application project (pom.xml)",
			})
		} else if base == "build.gradle" || base == "build.gradle.kts" {
			res.Candidates = append(res.Candidates, EntryPointCandidate{
				RelativePath:     m,
				Language:         "java",
				ExecutionMode:    ExecutionModeApplication,
				Confidence:       ConfidenceHigh,
				SuggestedCommand: "gradle bootRun",
				Score:            250,
				Reason:           "gradle application project",
			})
		}
	}
}
