package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxManifestBytes caps how much data we read from any single manifest (2 MB).
	maxManifestBytes = 2 * 1024 * 1024

	// maxScannedFiles caps total files examined during traversal to prevent unbounded walks.
	maxScannedFiles = 1000

	// maxTraverseDepth bounds the directory search depth from repository root.
	maxTraverseDepth = 3
)

var (
	// ignoredDirNames are directories skipped completely during repository inspection.
	ignoredDirNames = map[string]bool{
		".git":          true,
		".venv":         true,
		"venv":          true,
		"node_modules":  true,
		"__pycache__":   true,
		".pytest_cache": true,
		".mypy_cache":   true,
		".ruff_cache":   true,
		".idea":         true,
		".vscode":       true,
		"target":        true,
		"build":         true,
		"dist":          true,
		"vendor":        true,
		".cache":        true,
	}

	// manifestTargetFiles defines the specific manifest filenames we seek.
	manifestTargetFiles = map[string]ManifestType{
		"requirements.txt":   ManifestRequirementsTxt,
		"pyproject.toml":     ManifestPyprojectToml,
		"Pipfile":            ManifestPipfile,
		"poetry.lock":        ManifestPoetryLock,
		"uv.lock":            ManifestUvLock,
		"setup.py":           ManifestSetupPy,
		"setup.cfg":          ManifestSetupCfg,
		"package.json":       ManifestPackageJSON,
		"go.mod":             ManifestGoMod,
		"pom.xml":            ManifestPomXML,
		"build.gradle":       ManifestBuildGradle,
		"build.gradle.kts":   ManifestBuildGradleKts,
	}
)

// RepositoryAnalyzer performs static, bounded inspection of a repository.
type RepositoryAnalyzer struct{}

// NewRepositoryAnalyzer creates a new RepositoryAnalyzer.
func NewRepositoryAnalyzer() *RepositoryAnalyzer {
	return &RepositoryAnalyzer{}
}

// InspectRepository executes bounded, read-only inspection of repoDir.
// It enforces symlink boundary checks and returns a typed RepositoryAnalysis.
func (a *RepositoryAnalyzer) InspectRepository(ctx context.Context, repoDir string) (*RepositoryAnalysis, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("repository analyzer: repoDir cannot be empty")
	}

	cleanRoot, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("repository analyzer: resolving absolute path: %w", err)
	}

	rootInfo, err := os.Stat(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("repository analyzer: accessing repoDir: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("repository analyzer: repoDir %s is not a directory", cleanRoot)
	}

	canonicalRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		canonicalRoot = cleanRoot
	}

	analysis := &RepositoryAnalysis{
		PrimaryLanguage:    "unknown",
		Category:           CategoryUnknown,
		CategoryConfidence: ConfidenceLow,
		AnalyzedAt:         time.Now().UTC(),
		Manifests:          make([]DependencyManifest, 0),
		DiscoveredPackages: make([]string, 0),
		RequiresSystemLibs: make([]string, 0),
		Warnings:           make([]string, 0),
	}

	// 1. Check for Dockerfile at repository root
	dockerfiles := []string{"Dockerfile", "dockerfile", "Dockerfile.prod"}
	for _, df := range dockerfiles {
		candidate := filepath.Join(cleanRoot, df)
		if info, err := os.Lstat(candidate); err == nil && !info.IsDir() {
			if a.isSafePath(cleanRoot, canonicalRoot, candidate) {
				analysis.HasDockerfile = true
				analysis.DockerfilePath = df
				break
			}
		}
	}

	// 2. Discover manifest files safely
	discoveredManifests := make(map[string]ManifestType)
	scannedCount := 0
	visitedPaths := make(map[string]bool)

	var walkFn func(currentDir string, depth int) error
	walkFn = func(currentDir string, depth int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if depth > maxTraverseDepth || scannedCount >= maxScannedFiles {
			return nil
		}

		entries, err := os.ReadDir(currentDir)
		if err != nil {
			analysis.Warnings = append(analysis.Warnings, fmt.Sprintf("cannot read directory %s: %v", currentDir, err))
			return nil
		}

		for _, entry := range entries {
			scannedCount++
			if scannedCount >= maxScannedFiles {
				analysis.Warnings = append(analysis.Warnings, fmt.Sprintf("reached maximum file scan limit (%d files)", maxScannedFiles))
				return nil
			}

			name := entry.Name()
			fullPath := filepath.Join(currentDir, name)

			// Protect against symlink escapes
			if !a.isSafePath(cleanRoot, canonicalRoot, fullPath) {
				analysis.Warnings = append(analysis.Warnings, fmt.Sprintf("skipping unsafe symlink or external path: %s", name))
				continue
			}

			// Prevent cyclic traversal
			canonicalPath, evalErr := filepath.EvalSymlinks(fullPath)
			if evalErr == nil {
				if visitedPaths[canonicalPath] {
					continue
				}
				visitedPaths[canonicalPath] = true
			}

			if entry.IsDir() {
				if ignoredDirNames[name] || strings.HasPrefix(name, ".") {
					continue
				}
				if err := walkFn(fullPath, depth+1); err != nil {
					return err
				}
				continue
			}

			// Match target manifest
			if mType, ok := manifestTargetFiles[name]; ok {
				relPath, _ := filepath.Rel(cleanRoot, fullPath)
				discoveredManifests[fullPath] = mType
				_ = relPath
			}
		}
		return nil
	}

	if err := walkFn(cleanRoot, 0); err != nil {
		return nil, fmt.Errorf("repository analyzer: file scan error: %w", err)
	}

	// 3. Parse discovered manifests
	aggregatedPythonPackages := make(map[string]string)
	hasCudaIndex := false
	detectedLanguages := make(map[string]int)

	for fullPath, mType := range discoveredManifests {
		relPath, _ := filepath.Rel(cleanRoot, fullPath)
		manifest := a.parseManifestFile(fullPath, relPath, mType)
		analysis.Manifests = append(analysis.Manifests, manifest)

		// Language tally
		switch mType {
		case ManifestRequirementsTxt, ManifestPyprojectToml, ManifestPipfile, ManifestPoetryLock, ManifestUvLock, ManifestSetupPy, ManifestSetupCfg:
			detectedLanguages["python"]++
			for pkg, ver := range manifest.Dependencies {
				norm := NormalizePythonPackage(pkg)
				if existingVer, exists := aggregatedPythonPackages[norm]; exists {
					if existingVer != "" && ver != "" && existingVer != ver {
						analysis.Warnings = append(analysis.Warnings, fmt.Sprintf("conflicting version constraint for %s: %s vs %s", norm, existingVer, ver))
					}
				}
				aggregatedPythonPackages[norm] = ver
			}
			if manifest.Environment["cuda_index"] != "" {
				hasCudaIndex = true
			}
		case ManifestPackageJSON:
			detectedLanguages["nodejs"]++
		case ManifestGoMod:
			detectedLanguages["golang"]++
		case ManifestPomXML, ManifestBuildGradle, ManifestBuildGradleKts:
			detectedLanguages["java"]++
		}
	}

	// 4. Resolve Primary Language
	analysis.PrimaryLanguage = a.determinePrimaryLanguage(detectedLanguages, cleanRoot)

	// 5. Categorize Project
	if analysis.PrimaryLanguage == "python" {
		category, confidence, hasCUDA, sysLibs := classifyPythonProject(aggregatedPythonPackages, hasCudaIndex)
		analysis.Category = category
		analysis.CategoryConfidence = confidence
		analysis.HasCUDADependency = hasCUDA
		analysis.RequiresSystemLibs = sysLibs
		analysis.DiscoveredPackages = deduplicateAndSortPackages(aggregatedPythonPackages)
	} else if analysis.PrimaryLanguage == "nodejs" {
		analysis.Category = CategoryNodeJS
		analysis.CategoryConfidence = ConfidenceHigh
	} else if analysis.PrimaryLanguage == "golang" {
		analysis.Category = CategoryGolang
		analysis.CategoryConfidence = ConfidenceHigh
	} else if analysis.PrimaryLanguage == "java" {
		analysis.Category = CategoryJava
		analysis.CategoryConfidence = ConfidenceHigh
	} else if analysis.PrimaryLanguage == "c" {
		analysis.Category = CategoryC
		analysis.CategoryConfidence = ConfidenceHigh
	} else if analysis.PrimaryLanguage == "cpp" {
		analysis.Category = CategoryCpp
		analysis.CategoryConfidence = ConfidenceHigh
	} else if analysis.PrimaryLanguage == "rust" {
		analysis.Category = CategoryRust
		analysis.CategoryConfidence = ConfidenceHigh
	} else {
		analysis.Category = CategoryUnknown
		analysis.CategoryConfidence = ConfidenceLow
	}

	return analysis, nil
}

// parseManifestFile safely opens and limits the read size of a manifest file.
func (a *RepositoryAnalyzer) parseManifestFile(fullPath, relPath string, mType ManifestType) DependencyManifest {
	f, err := os.Open(fullPath)
	if err != nil {
		return DependencyManifest{
			Type:         mType,
			RelativePath: relPath,
			IsMalformed:  true,
			ParseError:   err.Error(),
		}
	}
	defer f.Close()

	// Bound memory read to maxManifestBytes
	limitedReader := io.LimitReader(f, maxManifestBytes)

	switch mType {
	case ManifestRequirementsTxt:
		return parseRequirementsTxt(limitedReader, relPath)
	case ManifestPyprojectToml:
		return parsePyprojectToml(limitedReader, relPath)
	case ManifestPipfile:
		return parsePipfile(limitedReader, relPath)
	case ManifestPoetryLock, ManifestUvLock:
		return parseLockfilePackages(limitedReader, mType, relPath)
	case ManifestSetupPy, ManifestSetupCfg:
		return parseSetupPyOrCfg(limitedReader, mType, relPath)
	case ManifestPackageJSON:
		return parsePackageJSON(limitedReader, relPath)
	case ManifestGoMod:
		return parseGoMod(limitedReader, relPath)
	case ManifestPomXML, ManifestBuildGradle, ManifestBuildGradleKts:
		return parseJavaManifest(limitedReader, mType, relPath)
	default:
		return DependencyManifest{
			Type:         mType,
			RelativePath: relPath,
		}
	}
}

// isSafePath ensures target is within cleanRoot and does not escape via symlinks.
func (a *RepositoryAnalyzer) isSafePath(cleanRoot, canonicalRoot, target string) bool {
	// Lexical check
	cleanTarget := filepath.Clean(target)
	if !strings.HasPrefix(cleanTarget, cleanRoot) {
		return false
	}

	// Canonical evaluation
	realTarget, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		// If file doesn't exist yet, check its directory
		realDir, dirErr := filepath.EvalSymlinks(filepath.Dir(cleanTarget))
		if dirErr == nil {
			return strings.HasPrefix(realDir, canonicalRoot)
		}
		return false
	}

	return strings.HasPrefix(realTarget, canonicalRoot)
}

// determinePrimaryLanguage resolves the dominant language when multiple manifests exist.
func (a *RepositoryAnalyzer) determinePrimaryLanguage(tally map[string]int, cleanRoot string) string {
	if len(tally) == 0 {
		// Fallback: check file extensions in root
		if fileExistsWithExt(cleanRoot, ".py") {
			return "python"
		}
		if fileExistsWithExt(cleanRoot, ".js", ".ts", ".jsx", ".tsx") {
			return "nodejs"
		}
		if fileExistsWithExt(cleanRoot, ".go") {
			return "golang"
		}
		if fileExistsWithExt(cleanRoot, ".java") {
			return "java"
		}
		if fileExistsWithExt(cleanRoot, ".c") {
			return "c"
		}
		if fileExistsWithExt(cleanRoot, ".cpp", ".cc", ".cxx", ".C") {
			return "cpp"
		}
		if fileExistsWithExt(cleanRoot, ".rs") {
			return "rust"
		}
		return "unknown"
	}

	bestLang := "unknown"
	bestCount := -1

	// Explicit language priority if counts match: python > nodejs > golang > java > cpp > c > rust
	order := []string{"python", "nodejs", "golang", "java", "cpp", "c", "rust"}
	for _, lang := range order {
		count := tally[lang]
		if count > bestCount {
			bestCount = count
			bestLang = lang
		}
	}

	return bestLang
}

// fileExistsWithExt checks if any regular file in dir has one of the target extensions.
func fileExistsWithExt(dir string, exts ...string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(e.Name()), ext) {
				return true
			}
		}
	}
	return false
}
