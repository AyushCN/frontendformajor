package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// LanguageAdapter abstracts language-specific entry-point discovery, execution mode classification,
// and command generation.
type LanguageAdapter interface {
	// Language returns the canonical name of the language (e.g. "python", "c", "cpp", "nodejs", "golang", "java", "rust").
	Language() string

	// Extensions returns the list of file extensions handled by this adapter.
	Extensions() []string

	// IsCompiled indicates whether the language requires compilation before execution.
	IsCompiled() bool

	// InspectFile inspects a source file statically without executing code.
	InspectFile(ctx context.Context, file ScannedFile, content []byte, isSingleSource bool) (*EntryPointCandidate, error)

	// BuildCommand returns the compile command if IsCompiled is true, using buildDir for binary output.
	BuildCommand(candidate EntryPointCandidate, buildDir string) string

	// RunCommand returns the execution command for the file or compiled binary.
	RunCommand(candidate EntryPointCandidate, binaryPath string) string
}

// AdapterRegistry manages the available language adapters.
type AdapterRegistry struct {
	mu             sync.RWMutex
	adaptersByLang map[string]LanguageAdapter
	adaptersByExt  map[string]LanguageAdapter
	adapters       []LanguageAdapter
}

// NewAdapterRegistry creates an initialized registry with built-in language adapters.
func NewAdapterRegistry() *AdapterRegistry {
	r := &AdapterRegistry{
		adaptersByLang: make(map[string]LanguageAdapter),
		adaptersByExt:  make(map[string]LanguageAdapter),
		adapters:       make([]LanguageAdapter, 0),
	}

	// Register built-in adapters
	r.Register(&PythonAdapter{})
	r.Register(&CAdapter{})
	r.Register(&CppAdapter{})
	r.Register(&NodeAdapter{})
	r.Register(&GoAdapter{})
	r.Register(&JavaAdapter{})
	r.Register(&RustAdapter{})

	return r
}

// Register adds or updates an adapter in the registry.
func (r *AdapterRegistry) Register(adapter LanguageAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	lang := strings.ToLower(adapter.Language())
	r.adaptersByLang[lang] = adapter

	for _, ext := range adapter.Extensions() {
		r.adaptersByExt[strings.ToLower(ext)] = adapter
	}

	r.adapters = append(r.adapters, adapter)
}

// GetByLanguage retrieves the adapter for a given language.
func (r *AdapterRegistry) GetByLanguage(lang string) (LanguageAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adaptersByLang[strings.ToLower(lang)]
	return a, ok
}

// GetByExtension retrieves the adapter for a given file extension.
func (r *AdapterRegistry) GetByExtension(ext string) (LanguageAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adaptersByExt[strings.ToLower(ext)]
	return a, ok
}

// All returns all registered adapters.
func (r *AdapterRegistry) All() []LanguageAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LanguageAdapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// ----------------------------------------------------------------------------
// Python Adapter
// ----------------------------------------------------------------------------

type frameworkRule struct {
	name    string
	pattern *regexp.Regexp
}

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

	pyConventionalRank = map[string]int{
		"main.py":   50,
		"app.py":    45,
		"server.py": 40,
		"start.py":  35,
		"run.py":    30,
		"bot.py":    25,
		"index.py":  20,
		"hello.py":  15,
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
		"index.js":  50,
		"index.ts":  50,
		"index.mjs": 45,
		"server.js": 40,
		"server.ts": 40,
		"app.js":    35,
		"app.ts":    35,
		"main.js":   30,
		"main.ts":   30,
		"start.js":  25,
	}

	// Go patterns
	goHTTPPatterns        = regexp.MustCompile(`(?m)(http\.ListenAndServe|gin\.Default|fiber\.New|echo\.New)`)
	goInteractivePatterns = regexp.MustCompile(`(?m)(fmt\.Scan|bufio\.NewReader\s*\(\s*os\.Stdin\s*\)|os\.Stdin\.Read)`)
)

type PythonAdapter struct{}

func (a *PythonAdapter) Language() string     { return "python" }
func (a *PythonAdapter) Extensions() []string { return []string{".py"} }
func (a *PythonAdapter) IsCompiled() bool     { return false }

func (a *PythonAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	lowerName := strings.ToLower(name)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "python",
		SuggestedCommand: fmt.Sprintf("python %s", file.RelPath),
		Confidence:       ConfidenceMedium,
		Score:            50, // Base score for arbitrary python file (e.g. g.py, temp.py)
		Reason:           fmt.Sprintf("python script %q", file.RelPath),
	}

	// Conventional filename boost
	if rank, ok := pyConventionalRank[lowerName]; ok {
		candidate.Score += rank
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("conventional entry-point name %q", name)
	}

	// Single-source bonus
	if isSingleSource {
		candidate.Score += 60
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = "single python file in repository"
	}

	// Static content inspection: Framework detection
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

	// Interactive input detection
	if pyInteractivePatterns.Match(content) {
		candidate.HasInteractive = true
		if candidate.ExecutionMode != ExecutionModeApplication {
			candidate.ExecutionMode = ExecutionModeInteractiveScript
			candidate.Score += 15
			candidate.Reason = "script requests interactive standard input"
		}
	}

	// Main block detection
	if pyMainBlockPattern.Match(content) {
		candidate.Score += 25
	}

	// Penalize tests
	if strings.HasPrefix(lowerName, "test_") || strings.HasSuffix(lowerName, "_test.py") || lowerName == "tests.py" {
		candidate.Score -= 80
	}

	if candidate.ExecutionMode == "" {
		candidate.ExecutionMode = ExecutionModeScript
	}

	return candidate, nil
}

func (a *PythonAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	return ""
}

func (a *PythonAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	return fmt.Sprintf("python %s", candidate.RelativePath)
}

// ----------------------------------------------------------------------------
// C Adapter
// ----------------------------------------------------------------------------

type CAdapter struct{}

func (a *CAdapter) Language() string     { return "c" }
func (a *CAdapter) Extensions() []string { return []string{".c"} }
func (a *CAdapter) IsCompiled() bool     { return true }

var (
	cMainPattern        = regexp.MustCompile(`(?m)(int|void)\s+main\s*\(`)
	cInteractivePattern = regexp.MustCompile(`(?m)(scanf\s*\(|getchar\s*\(|fgets\s*\(|readline\s*\()`)
	cServerPattern      = regexp.MustCompile(`(?m)(socket\s*\(|bind\s*\(|listen\s*\(|accept\s*\()`)
)

func (a *CAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	lowerName := strings.ToLower(name)
	base := strings.TrimSuffix(name, filepath.Ext(name))

	hasMain := cMainPattern.Match(content)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "c",
		SuggestedCommand: fmt.Sprintf("./%s_bin", base),
		Confidence:       ConfidenceMedium,
		Score:            20,
		Reason:           fmt.Sprintf("C source file %q", file.RelPath),
	}

	if hasMain {
		candidate.Score += 80 // Primary executable indicator
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("C source %q with main() function", file.RelPath)
	} else {
		// Does not contain main() function; likely a helper module or library
		candidate.Score -= 40
		candidate.Reason = fmt.Sprintf("C source %q without main() entry point", file.RelPath)
	}

	if isSingleSource {
		candidate.Score += 60
		candidate.Confidence = ConfidenceHigh
		if hasMain {
			candidate.Reason = "single C source file with main() entry point"
		}
	}

	if lowerName == "main.c" {
		candidate.Score += 30
	}

	// Interactive vs Application vs Script
	if cServerPattern.Match(content) {
		candidate.ExecutionMode = ExecutionModeApplication
		candidate.Reason = "detected C network socket/server implementation"
	} else if cInteractivePattern.Match(content) {
		candidate.HasInteractive = true
		candidate.ExecutionMode = ExecutionModeInteractiveScript
		candidate.Reason = "C program requests interactive input (scanf/getchar/fgets)"
	} else {
		candidate.ExecutionMode = ExecutionModeScript
	}

	return candidate, nil
}

func (a *CAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	binName := base + "_bin"
	outPath := filepath.Join(buildDir, binName)
	return fmt.Sprintf("gcc -O2 -Wall -o %s %s", outPath, candidate.RelativePath)
}

func (a *CAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	if binaryPath != "" {
		return binaryPath
	}
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	return fmt.Sprintf("./%s_bin", base)
}

// ----------------------------------------------------------------------------
// C++ Adapter
// ----------------------------------------------------------------------------

type CppAdapter struct{}

func (a *CppAdapter) Language() string { return "cpp" }
func (a *CppAdapter) Extensions() []string {
	return []string{".cpp", ".cc", ".cxx"}
}
func (a *CppAdapter) IsCompiled() bool { return true }

var (
	cppMainPattern        = regexp.MustCompile(`(?m)(int|void)\s+main\s*\(`)
	cppInteractivePattern = regexp.MustCompile(`(?m)(std::cin|cin\s*>>|getline\s*\(\s*cin|scanf\s*\()`)
	cppServerPattern      = regexp.MustCompile(`(?m)(httplib|crow::SimpleApp|boost::asio|socket\s*\()`)
)

func (a *CppAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	lowerName := strings.ToLower(name)
	base := strings.TrimSuffix(name, filepath.Ext(name))

	hasMain := cppMainPattern.Match(content)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "cpp",
		SuggestedCommand: fmt.Sprintf("./%s_bin", base),
		Confidence:       ConfidenceMedium,
		Score:            20,
		Reason:           fmt.Sprintf("C++ source file %q", file.RelPath),
	}

	if hasMain {
		candidate.Score += 80
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("C++ source %q with main() function", file.RelPath)
	} else {
		candidate.Score -= 40
		candidate.Reason = fmt.Sprintf("C++ source %q without main() entry point", file.RelPath)
	}

	if isSingleSource {
		candidate.Score += 60
		candidate.Confidence = ConfidenceHigh
		if hasMain {
			candidate.Reason = "single C++ source file with main() entry point"
		}
	}

	if lowerName == "main.cpp" || lowerName == "main.cc" {
		candidate.Score += 30
	}

	if cppServerPattern.Match(content) {
		candidate.ExecutionMode = ExecutionModeApplication
		candidate.Reason = "detected C++ server framework or network socket"
	} else if cppInteractivePattern.Match(content) {
		candidate.HasInteractive = true
		candidate.ExecutionMode = ExecutionModeInteractiveScript
		candidate.Reason = "C++ program requests interactive input (std::cin)"
	} else {
		candidate.ExecutionMode = ExecutionModeScript
	}

	return candidate, nil
}

func (a *CppAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	binName := base + "_bin"
	outPath := filepath.Join(buildDir, binName)
	return fmt.Sprintf("g++ -O2 -Wall -std=c++17 -o %s %s", outPath, candidate.RelativePath)
}

func (a *CppAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	if binaryPath != "" {
		return binaryPath
	}
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	return fmt.Sprintf("./%s_bin", base)
}

// ----------------------------------------------------------------------------
// Node.js Adapter
// ----------------------------------------------------------------------------

type NodeAdapter struct{}

func (a *NodeAdapter) Language() string { return "nodejs" }
func (a *NodeAdapter) Extensions() []string {
	return []string{".js", ".ts", ".mjs", ".cjs"}
}
func (a *NodeAdapter) IsCompiled() bool { return false }

func (a *NodeAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	lowerName := strings.ToLower(name)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "nodejs",
		SuggestedCommand: fmt.Sprintf("node %s", file.RelPath),
		ExecutionMode:    ExecutionModeScript,
		Confidence:       ConfidenceMedium,
		Score:            50,
		Reason:           fmt.Sprintf("JavaScript/TypeScript script %q", file.RelPath),
	}

	if rank, ok := nodeConventionalRank[lowerName]; ok {
		candidate.Score += rank
		candidate.Confidence = ConfidenceHigh
	}

	if isSingleSource {
		candidate.Score += 60
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = "single JavaScript/TypeScript file in repository"
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
		candidate.Reason = "script requests interactive stdin"
	}

	return candidate, nil
}

func (a *NodeAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	return ""
}

func (a *NodeAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	return fmt.Sprintf("node %s", candidate.RelativePath)
}

// ----------------------------------------------------------------------------
// Go Adapter
// ----------------------------------------------------------------------------

type GoAdapter struct{}

func (a *GoAdapter) Language() string     { return "golang" }
func (a *GoAdapter) Extensions() []string { return []string{".go"} }
func (a *GoAdapter) IsCompiled() bool     { return true }

func (a *GoAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	hasPackageMain := strings.Contains(string(content), "package main")
	hasFuncMain := strings.Contains(string(content), "func main()")

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "golang",
		SuggestedCommand: fmt.Sprintf("go run %s", file.RelPath),
		ExecutionMode:    ExecutionModeScript,
		Confidence:       ConfidenceMedium,
		Score:            20,
		Reason:           fmt.Sprintf("Go source file %q", file.RelPath),
	}

	if hasPackageMain && hasFuncMain {
		candidate.Score += 80
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("Go main package in %s", file.RelPath)
	} else {
		candidate.Score -= 40
	}

	if isSingleSource {
		candidate.Score += 50
	}

	if name == "main.go" {
		candidate.Score += 30
	}

	if goHTTPPatterns.Match(content) {
		candidate.ExecutionMode = ExecutionModeApplication
		candidate.Reason = "detected Go HTTP web server"
	} else if goInteractivePatterns.Match(content) {
		candidate.HasInteractive = true
		candidate.ExecutionMode = ExecutionModeInteractiveScript
		candidate.Reason = "Go program requests standard input"
	}

	return candidate, nil
}

func (a *GoAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	outPath := filepath.Join(buildDir, base+"_bin")
	return fmt.Sprintf("go build -o %s %s", outPath, candidate.RelativePath)
}

func (a *GoAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	if binaryPath != "" {
		return binaryPath
	}
	return fmt.Sprintf("go run %s", candidate.RelativePath)
}

// ----------------------------------------------------------------------------
// Java Adapter
// ----------------------------------------------------------------------------

type JavaAdapter struct{}

func (a *JavaAdapter) Language() string     { return "java" }
func (a *JavaAdapter) Extensions() []string { return []string{".java"} }
func (a *JavaAdapter) IsCompiled() bool     { return true }

var javaMainPattern = regexp.MustCompile(`(?m)public\s+static\s+void\s+main\s*\(\s*String`)

func (a *JavaAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	hasMain := javaMainPattern.Match(content)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "java",
		SuggestedCommand: fmt.Sprintf("java %s", file.RelPath),
		ExecutionMode:    ExecutionModeScript,
		Confidence:       ConfidenceMedium,
		Score:            20,
		Reason:           fmt.Sprintf("Java source file %q", file.RelPath),
	}

	if hasMain {
		candidate.Score += 80
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("Java class %s with main() method", base)
	}

	if isSingleSource {
		candidate.Score += 50
	}

	return candidate, nil
}

func (a *JavaAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	return fmt.Sprintf("javac -d %s %s", buildDir, candidate.RelativePath)
}

func (a *JavaAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	return fmt.Sprintf("java %s", base)
}

// ----------------------------------------------------------------------------
// Rust Adapter
// ----------------------------------------------------------------------------

type RustAdapter struct{}

func (a *RustAdapter) Language() string     { return "rust" }
func (a *RustAdapter) Extensions() []string { return []string{".rs"} }
func (a *RustAdapter) IsCompiled() bool     { return true }

var (
	rustMainPattern        = regexp.MustCompile(`(?m)fn\s+main\s*\(`)
	rustInteractivePattern = regexp.MustCompile(`(?m)(std::io::stdin|io::stdin\s*\()`)
	rustServerPattern      = regexp.MustCompile(`(?m)(actix_web|axum|rocket|warp)`)
)

func (a *RustAdapter) InspectFile(
	ctx context.Context,
	file ScannedFile,
	content []byte,
	isSingleSource bool,
) (*EntryPointCandidate, error) {
	name := filepath.Base(file.RelPath)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	hasMain := rustMainPattern.Match(content)

	candidate := &EntryPointCandidate{
		RelativePath:     file.RelPath,
		Language:         "rust",
		SuggestedCommand: fmt.Sprintf("./%s_bin", base),
		ExecutionMode:    ExecutionModeScript,
		Confidence:       ConfidenceMedium,
		Score:            20,
		Reason:           fmt.Sprintf("Rust source file %q", file.RelPath),
	}

	if hasMain {
		candidate.Score += 80
		candidate.Confidence = ConfidenceHigh
		candidate.Reason = fmt.Sprintf("Rust source %q with fn main()", file.RelPath)
	} else {
		candidate.Score -= 40
	}

	if isSingleSource {
		candidate.Score += 60
	}

	if name == "main.rs" {
		candidate.Score += 30
	}

	if rustServerPattern.Match(content) {
		candidate.ExecutionMode = ExecutionModeApplication
		candidate.Reason = "detected Rust web framework"
	} else if rustInteractivePattern.Match(content) {
		candidate.HasInteractive = true
		candidate.ExecutionMode = ExecutionModeInteractiveScript
		candidate.Reason = "Rust program requests standard input"
	}

	return candidate, nil
}

func (a *RustAdapter) BuildCommand(candidate EntryPointCandidate, buildDir string) string {
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	outPath := filepath.Join(buildDir, base+"_bin")
	return fmt.Sprintf("rustc -O -o %s %s", outPath, candidate.RelativePath)
}

func (a *RustAdapter) RunCommand(candidate EntryPointCandidate, binaryPath string) string {
	if binaryPath != "" {
		return binaryPath
	}
	base := strings.TrimSuffix(filepath.Base(candidate.RelativePath), filepath.Ext(candidate.RelativePath))
	return fmt.Sprintf("./%s_bin", base)
}
