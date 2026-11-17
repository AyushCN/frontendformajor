package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ExecutionMode defines the target operational model for code execution.
type ExecutionMode string

const (
	ExecutionModeScript            ExecutionMode = "SCRIPT"
	ExecutionModeInteractiveScript ExecutionMode = "INTERACTIVE_SCRIPT"
	ExecutionModeApplication       ExecutionMode = "APPLICATION"
	ExecutionModeAmbiguous         ExecutionMode = "AMBIGUOUS"
	ExecutionModeUnsupported       ExecutionMode = "UNSUPPORTED"
)

// EntryPointCandidate represents a potential executable file discovered in the repository.
type EntryPointCandidate struct {
	RelativePath     string          `json:"relative_path"`
	Language         string          `json:"language"`
	ExecutionMode    ExecutionMode   `json:"execution_mode"`
	Confidence       ConfidenceLevel `json:"confidence"`
	SuggestedCommand string          `json:"suggested_command"`
	Score            int             `json:"score"`
	Reason           string          `json:"reason"`
	Framework        string          `json:"framework,omitempty"`
	HasInteractive   bool            `json:"has_interactive,omitempty"`
}

// EntryPointResolution captures the ranked candidates, selected start command, and execution mode.
type EntryPointResolution struct {
	PrimaryCandidate *EntryPointCandidate  `json:"primary_candidate,omitempty"`
	Candidates       []EntryPointCandidate `json:"candidates"`
	SelectedCommand  string                `json:"selected_command"`
	CommandSource    string                `json:"command_source"` // "USER_EXPLICIT", "PROJECT_CONFIG", "FRAMEWORK_CONVENTION", "CONVENTIONAL_FILENAME", "SINGLE_FILE_INFERENCE", "AMBIGUOUS"
	ExecutionMode    ExecutionMode         `json:"execution_mode"`
	Confidence       ConfidenceLevel       `json:"confidence"`
	IsAmbiguous      bool                  `json:"is_ambiguous"`
	Warnings         []string              `json:"warnings,omitempty"`
}

// EntryPointResolver inspects repositories to discover executable entry points and deduce execution mode.
type EntryPointResolver struct {
	maxInspectBytes int64
}

// NewEntryPointResolver creates a new EntryPointResolver.
func NewEntryPointResolver() *EntryPointResolver {
	return &EntryPointResolver{
		maxInspectBytes: 64 * 1024, // 64 KB bounded read per source file
	}
}

type frameworkRule struct {
	name    string
	pattern *regexp.Regexp
}

// Python inspection regexes
var (
	pyFrameworkRules = []frameworkRule{
		{"Flask", regexp.MustCompile(`(?m)(from\s+flask\s+import|import\s+flask|Flask\s*\()`)},
		{"FastAPI", regexp.MustCompile(`(?m)(from\s+fastapi\s+import|import\s+fastapi|FastAPI\s*\()`)},
		{"Django", regexp.MustCompile(`(?m)(from\s+django|import\s+django|DJANGO_SETTINGS_MODULE)`)},
		{"Tornado", regexp.MustCompile(`(?m)(import\s+tornado|tornado\.web\.Application)`)},
		{"Aiohttp", regexp.MustCompile(`(?m)(import\s+aiohttp|aiohttp\.web\.Application)`)},
		{"Streamlit", regexp.MustCompile(`(?m)(import\s+streamlit|from\s+streamlit)`)},
		{"Gradio", regexp.MustCompile(`(?m)(import\s+gradio|from\s+gradio)`)},
		{"Bottle", regexp.MustCompile(`(?m)(from\s+bottle\s+import|import\s+bottle|bottle\.run)`)},
		{"Uvicorn", regexp.MustCompile(`(?m)(uvicorn\.run)`)},
	}

	pyInteractivePatterns = regexp.MustCompile(`(?m)(input\s*\(|sys\.stdin\.read|sys\.stdin\.readline|getpass\.getpass)`)
	pyMainBlockPattern    = regexp.MustCompile(`(?m)if\s+__name__\s*==\s*['"]__main__['"]\s*:`)

	// Conventional filenames for Python
	pyConventionalRank = map[string]int{
		"main.py":   100,
		"app.py":    95,
		"server.py": 85,
		"start.py":  80,
		"run.py":    75,
		"bot.py":    70,
		"index.py":  65,
		"hello.py":  60,
	}

	// Node.js patterns
	nodeFrameworkRules = []frameworkRule{
		{"Express", regexp.MustCompile(`(?m)(require\s*\(\s*['"]express['"]\s*\)|from\s+['"]express['"]|express\s*\(\s*\))`)},
		{"Fastify", regexp.MustCompile(`(?m)(require\s*\(\s*['"]fastify['"]\s*\)|from\s+['"]fastify['"])`)},
		{"Koa", regexp.MustCompile(`(?m)(require\s*\(\s*['"]koa['"]\s*\)|from\s+['"]koa['"])`)},
		{"Next", regexp.MustCompile(`(?m)(next\s+start|next\s+dev)`)},
		{"HTTP", regexp.MustCompile(`(?m)(http\.createServer\s*\()`)},
	}
	nodeInteractivePatterns = regexp.MustCompile(`(?m)(readline\.createInterface|process\.stdin)`)

	nodeConventionalRank = map[string]int{
		"index.js":  100,
		"index.ts":  100,
		"index.mjs": 95,
		"server.js": 90,
		"server.ts": 90,
		"app.js":    85,
		"app.ts":    85,
		"main.js":   80,
		"main.ts":   80,
		"start.js":  75,
	}

	// Go patterns
	goHTTPPatterns        = regexp.MustCompile(`(?m)(http\.ListenAndServe|gin\.Default|fiber\.New|echo\.New)`)
	goInteractivePatterns = regexp.MustCompile(`(?m)(fmt\.Scan|bufio\.NewReader\s*\(\s*os\.Stdin\s*\)|os\.Stdin\.Read)`)
)

// ResolveEntryPoints analyzes the repository files and user configuration to determine
// the optimal executable entry point and execution mode.
func (r *EntryPointResolver) ResolveEntryPoints(
	ctx context.Context,
	repoDir string,
	analysis *RepositoryAnalysis,
	userCfg *UserBuildConfig,
) (*EntryPointResolution, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("entry point resolver: repoDir cannot be empty")
	}

	canonicalRoot, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		canonicalRoot = repoDir
	}

	res := &EntryPointResolution{
		Candidates:    make([]EntryPointCandidate, 0),
		ExecutionMode: ExecutionModeUnsupported,
		Confidence:    ConfidenceLow,
		Warnings:      make([]string, 0),
	}

	// 1. Check for Precedence Tier 1: Explicit User Command
	if userCfg != nil && strings.TrimSpace(userCfg.StartCmd) != "" {
		userCmd := strings.TrimSpace(userCfg.StartCmd)
		res.SelectedCommand = userCmd
		res.CommandSource = "USER_EXPLICIT"
		res.Confidence = ConfidenceHigh
		res.ExecutionMode = ExecutionModeApplication // Explicit commands assume application/service unless overridden

		// Check if user command looks like a direct script run
		if strings.HasPrefix(userCmd, "python ") || strings.HasPrefix(userCmd, "node ") || strings.HasPrefix(userCmd, "go run ") {
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

	// 2. Check for Precedence Tier 2: Explicit Project Configuration
	// 2a. railpack.json
	if rpStart := readRailpackJSONStart(repoDir); rpStart != "" {
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

	// 2b. Procfile
	if procStart := readProcfileWeb(repoDir); procStart != "" {
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

	// 3. Language-specific entry point discovery
	lang := "unknown"
	if analysis != nil && analysis.PrimaryLanguage != "" && analysis.PrimaryLanguage != "unknown" {
		lang = analysis.PrimaryLanguage
	} else {
		// Infer language from root extensions
		lang = r.inferLanguageFromFiles(repoDir)
	}

	switch lang {
	case "python":
		r.discoverPythonEntryPoints(ctx, repoDir, canonicalRoot, res)
	case "nodejs":
		r.discoverNodeEntryPoints(ctx, repoDir, canonicalRoot, res)
	case "golang":
		r.discoverGoEntryPoints(ctx, repoDir, canonicalRoot, res)
	case "java":
		r.discoverJavaEntryPoints(ctx, repoDir, canonicalRoot, res)
	default:
		// Try inspecting all recognized types if language unknown
		r.discoverPythonEntryPoints(ctx, repoDir, canonicalRoot, res)
		if len(res.Candidates) == 0 {
			r.discoverNodeEntryPoints(ctx, repoDir, canonicalRoot, res)
		}
	}

	if len(res.Candidates) == 0 {
		res.ExecutionMode = ExecutionModeUnsupported
		res.Confidence = ConfidenceLow
		return res, nil
	}

	// Sort candidates by Score descending
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		return res.Candidates[i].Score > res.Candidates[j].Score
	})

	// 4. Evaluate Ambiguity and Select Primary Candidate
	topCandidate := res.Candidates[0]

	// If there are multiple candidates, check for ambiguity
	if len(res.Candidates) > 1 {
		secondCandidate := res.Candidates[1]
		// If both have identical scores and neither is a dominant conventional filename or framework
		if topCandidate.Score == secondCandidate.Score && topCandidate.Score < 90 {
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

	// Unambiguous primary candidate selected
	res.PrimaryCandidate = &topCandidate
	res.SelectedCommand = topCandidate.SuggestedCommand
	res.ExecutionMode = topCandidate.ExecutionMode
	res.Confidence = topCandidate.Confidence

	// Assign CommandSource
	if len(res.Candidates) == 1 && topCandidate.Reason == "single python file in repository" {
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

// discoverPythonEntryPoints searches for .py candidates and classifies their execution mode.
func (r *EntryPointResolver) discoverPythonEntryPoints(
	ctx context.Context,
	repoDir, canonicalRoot string,
	res *EntryPointResolution,
) {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("cannot read repo dir: %v", err))
		return
	}

	var pyFiles []string
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".py") {
			pyFiles = append(pyFiles, name)
		}
	}

	isSingleFile := len(pyFiles) == 1

	for _, name := range pyFiles {
		fullPath := filepath.Join(repoDir, name)

		// Symlink safety check
		if !r.isSafePath(repoDir, canonicalRoot, fullPath) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipping unsafe symlink %s", name))
			continue
		}

		// Inspect content up to maxInspectBytes
		content, err := r.readSnippet(fullPath)
		if err != nil {
			continue
		}

		// Calculate candidate score and attributes
		candidate := EntryPointCandidate{
			RelativePath:     name,
			Language:         "python",
			SuggestedCommand: fmt.Sprintf("python %s", name),
			Confidence:       ConfidenceMedium,
		}

		// Check conventional filename rank
		if rank, ok := pyConventionalRank[strings.ToLower(name)]; ok {
			candidate.Score += rank
			candidate.Reason = fmt.Sprintf("conventional entry-point name %q", name)
			candidate.Confidence = ConfidenceHigh
		} else {
			candidate.Score = 20
			candidate.Reason = fmt.Sprintf("python script %q", name)
		}

		// Single-file inference bonus
		if isSingleFile {
			candidate.Score += 60
			candidate.Confidence = ConfidenceHigh
			candidate.Reason = "single python file in repository"
		}

		// Static content inspection
		// 1. Framework detection (deterministic priority order)
		for _, rule := range pyFrameworkRules {
			if rule.pattern.Match(content) {
				candidate.Framework = rule.name
				candidate.Score += 40
				candidate.ExecutionMode = ExecutionModeApplication
				candidate.Confidence = ConfidenceHigh
				candidate.Reason = fmt.Sprintf("detected %s application framework", rule.name)
				break
			}
		}

		// 2. Interactive input detection
		if pyInteractivePatterns.Match(content) {
			candidate.HasInteractive = true
			if candidate.ExecutionMode != ExecutionModeApplication {
				candidate.ExecutionMode = ExecutionModeInteractiveScript
				candidate.Reason = "script requests interactive standard input"
				candidate.Score += 15
			}
		}

		// 3. Main block detection
		if pyMainBlockPattern.Match(content) {
			candidate.Score += 25
		}

		// 4. Penalize test files
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "test_") || strings.HasSuffix(lower, "_test.py") || lower == "tests.py" {
			candidate.Score -= 80
		}

		// Default execution mode if not application or interactive
		if candidate.ExecutionMode == "" {
			candidate.ExecutionMode = ExecutionModeScript
		}

		res.Candidates = append(res.Candidates, candidate)
	}
}

// discoverNodeEntryPoints searches for Node.js entry points.
func (r *EntryPointResolver) discoverNodeEntryPoints(
	ctx context.Context,
	repoDir, canonicalRoot string,
	res *EntryPointResolution,
) {
	// Check package.json scripts.start or main
	pkgJSONPath := filepath.Join(repoDir, "package.json")
	if data, err := os.ReadFile(pkgJSONPath); err == nil {
		var pkg struct {
			Main    string            `json:"main"`
			Scripts map[string]string `json:"scripts"`
		}
		if jsonErr := json.Unmarshal(data, &pkg); jsonErr == nil {
			if startCmd, ok := pkg.Scripts["start"]; ok && strings.TrimSpace(startCmd) != "" {
				res.Candidates = append(res.Candidates, EntryPointCandidate{
					RelativePath:     "package.json",
					Language:         "nodejs",
					ExecutionMode:    ExecutionModeApplication,
					Confidence:       ConfidenceHigh,
					SuggestedCommand: "npm start",
					Score:            150,
					Reason:           "package.json start script",
				})
			}
			if pkg.Main != "" {
				res.Candidates = append(res.Candidates, EntryPointCandidate{
					RelativePath:     pkg.Main,
					Language:         "nodejs",
					ExecutionMode:    ExecutionModeApplication,
					Confidence:       ConfidenceHigh,
					SuggestedCommand: fmt.Sprintf("node %s", pkg.Main),
					Score:            120,
					Reason:           "package.json main field",
				})
			}
		}
	}

	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".mjs") {
			fullPath := filepath.Join(repoDir, name)
			if !r.isSafePath(repoDir, canonicalRoot, fullPath) {
				continue
			}

			content, _ := r.readSnippet(fullPath)
			candidate := EntryPointCandidate{
				RelativePath:     name,
				Language:         "nodejs",
				SuggestedCommand: fmt.Sprintf("node %s", name),
				ExecutionMode:    ExecutionModeScript,
				Confidence:       ConfidenceMedium,
				Score:            20,
				Reason:           fmt.Sprintf("javascript/typescript source %q", name),
			}

			if rank, ok := nodeConventionalRank[lower]; ok {
				candidate.Score += rank
				candidate.Confidence = ConfidenceHigh
			}

			for _, rule := range nodeFrameworkRules {
				if rule.pattern.Match(content) {
					candidate.Framework = rule.name
					candidate.ExecutionMode = ExecutionModeApplication
					candidate.Score += 40
					candidate.Reason = fmt.Sprintf("detected %s server framework", rule.name)
					break
				}
			}

			if nodeInteractivePatterns.Match(content) && candidate.ExecutionMode != ExecutionModeApplication {
				candidate.HasInteractive = true
				candidate.ExecutionMode = ExecutionModeInteractiveScript
				candidate.Score += 15
			}

			res.Candidates = append(res.Candidates, candidate)
		}
	}
}

// discoverGoEntryPoints searches for Go main package entry points.
func (r *EntryPointResolver) discoverGoEntryPoints(
	ctx context.Context,
	repoDir, canonicalRoot string,
	res *EntryPointResolution,
) {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return
	}

	hasGoMod := false
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
		hasGoMod = true
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			fullPath := filepath.Join(repoDir, name)
			if !r.isSafePath(repoDir, canonicalRoot, fullPath) {
				continue
			}

			content, _ := r.readSnippet(fullPath)
			if strings.Contains(string(content), "package main") && strings.Contains(string(content), "func main()") {
				cmd := fmt.Sprintf("go run %s", name)
				if hasGoMod && name == "main.go" {
					cmd = "go run ."
				}

				mode := ExecutionModeScript
				if goHTTPPatterns.Match(content) {
					mode = ExecutionModeApplication
				} else if goInteractivePatterns.Match(content) {
					mode = ExecutionModeInteractiveScript
				}

				score := 50
				if name == "main.go" {
					score = 120
				}

				res.Candidates = append(res.Candidates, EntryPointCandidate{
					RelativePath:     name,
					Language:         "golang",
					ExecutionMode:    mode,
					Confidence:       ConfidenceHigh,
					SuggestedCommand: cmd,
					Score:            score,
					Reason:           fmt.Sprintf("go main package in %s", name),
				})
			}
		}
	}
}

// discoverJavaEntryPoints searches for Java application entry points.
func (r *EntryPointResolver) discoverJavaEntryPoints(
	ctx context.Context,
	repoDir, canonicalRoot string,
	res *EntryPointResolution,
) {
	hasPom := false
	if _, err := os.Stat(filepath.Join(repoDir, "pom.xml")); err == nil {
		hasPom = true
	}
	hasGradle := false
	if _, err := os.Stat(filepath.Join(repoDir, "build.gradle")); err == nil {
		hasGradle = true
	}

	if hasPom {
		res.Candidates = append(res.Candidates, EntryPointCandidate{
			RelativePath:     "pom.xml",
			Language:         "java",
			ExecutionMode:    ExecutionModeApplication,
			Confidence:       ConfidenceHigh,
			SuggestedCommand: "mvn spring-boot:run",
			Score:            100,
			Reason:           "maven application project",
		})
	} else if hasGradle {
		res.Candidates = append(res.Candidates, EntryPointCandidate{
			RelativePath:     "build.gradle",
			Language:         "java",
			ExecutionMode:    ExecutionModeApplication,
			Confidence:       ConfidenceHigh,
			SuggestedCommand: "gradle bootRun",
			Score:            100,
			Reason:           "gradle application project",
		})
	}
}

// inferLanguageFromFiles determines the primary language from root file extensions.
func (r *EntryPointResolver) inferLanguageFromFiles(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "unknown"
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".py") {
			return "python"
		}
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".ts") {
			return "nodejs"
		}
		if strings.HasSuffix(name, ".go") {
			return "golang"
		}
		if strings.HasSuffix(name, ".java") {
			return "java"
		}
	}
	return "unknown"
}

// readSnippet reads up to maxInspectBytes from path.
func (r *EntryPointResolver) readSnippet(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := io.LimitReader(f, r.maxInspectBytes)
	buf := make([]byte, r.maxInspectBytes)
	n, err := reader.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// isSafePath ensures target is within cleanRoot and does not escape via symlinks.
func (r *EntryPointResolver) isSafePath(cleanRoot, canonicalRoot, target string) bool {
	cleanTarget := filepath.Clean(target)
	if !strings.HasPrefix(cleanTarget, cleanRoot) {
		return false
	}
	realTarget, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		return false
	}
	return strings.HasPrefix(realTarget, canonicalRoot)
}
