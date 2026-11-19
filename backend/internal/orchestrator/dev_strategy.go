package orchestrator

import (
	"path/filepath"
	"strings"
)

// DevStrategy describes how a language/framework should behave inside a
// development container: what process to start, what files to watch, and
// whether a file change requires a restart or just a live reload by the
// framework's own watcher.
type DevStrategy interface {
	// Name returns a human-readable identifier used in logs and API responses.
	Name() string
	// Matches returns true if this strategy applies to the detected language and
	// framework, optionally inspecting the workspace file list.
	Matches(language, framework string, files []string) bool
	// WatchPatterns lists glob patterns for files whose changes should trigger a
	// reload notification (e.g. "*.py", "*.go"). The dev container's internal
	// watcher or the platform's exec signal path uses these.
	WatchPatterns() []string
	// IgnorePatterns lists patterns to exclude from watch (e.g. "node_modules",
	// "__pycache__", "*.pyc") to avoid spurious wakeups.
	IgnorePatterns() []string
	// DevCommand returns the command the dev container should run as its main
	// process. workDir is the in-container workspace path (always "/workspace").
	// port is the port the app should listen on. env is the decrypted KEY=VALUE
	// slice already supplied to the sandbox (may be nil).
	DevCommand(workDir string, port int, env []string) []string
	// BaseImage returns the Docker image to pull for the dev container base.
	// This is a plain upstream runtime image, NOT a Railpack/Nixpacks-built one.
	BaseImage() string
	// NeedsRestart reports whether a source-file change requires the process to
	// be restarted by the platform (true for compiled languages where the runtime
	// does not detect source changes on its own) or whether the runtime's own
	// watcher handles it (false for Flask --reload, node --watch, Next.js HMR).
	NeedsRestart() bool
	// DepsRequireRebuild returns true when the changed file list includes a
	// dependency manifest (requirements.txt, package.json, go.mod, etc.),
	// meaning a full image rebuild via the standard pipeline is recommended even
	// in dev mode.
	DepsRequireRebuild(changedFiles []string) bool
}

// ---- Registry ----------------------------------------------------------------

// devStrategyRegistry holds all known strategies in priority order. The first
// whose Matches() returns true wins. Framework-specific strategies are listed
// before language-generic ones so a Next.js project is not matched by the plain
// Node.js strategy.
var devStrategyRegistry = []DevStrategy{
	&NextJSStrategy{},
	&ReactStrategy{},
	&PythonUvicornStrategy{},
	&PythonFlaskStrategy{},
	&PythonDjangoStrategy{},
	&PythonGenericStrategy{},
	&NodeWatchStrategy{},
	&GoCompileStrategy{},
	&StaticHTMLStrategy{},
	&CCppStrategy{},
}

// SelectDevStrategy walks the registry and returns the first strategy that
// matches the given language and framework. Returns nil if none matches — the
// caller should fall back to the standard deployment pipeline.
func SelectDevStrategy(language, framework string, files []string) DevStrategy {
	lang := strings.ToLower(strings.TrimSpace(language))
	fw := strings.ToLower(strings.TrimSpace(framework))
	for _, s := range devStrategyRegistry {
		if s.Matches(lang, fw, files) {
			return s
		}
	}
	return nil
}

// ---- Shared helpers ----------------------------------------------------------

// hasFile reports whether the file list contains any of the given exact
// basenames. Used by DepsRequireRebuild implementations.
func hasFile(files []string, names ...string) bool {
	for _, f := range files {
		base := filepath.Base(f)
		for _, n := range names {
			if base == n {
				return true
			}
		}
	}
	return false
}

// ---- Strategies --------------------------------------------------------------

// StaticHTMLStrategy serves static files. No process restart is needed;
// a bind-mounted workspace means the browser can reload the page after save.
type StaticHTMLStrategy struct{}

func (s *StaticHTMLStrategy) Name() string { return "static-html" }

func (s *StaticHTMLStrategy) Matches(language, framework string, files []string) bool {
	return language == "html" || language == "static" ||
		framework == "static" || framework == "html"
}

func (s *StaticHTMLStrategy) WatchPatterns() []string {
	return []string{"*.html", "*.css", "*.js"}
}

func (s *StaticHTMLStrategy) IgnorePatterns() []string {
	return []string{"node_modules", ".git"}
}

// DevCommand uses Python's built-in HTTP server — present in every python:*-slim
// image, zero additional deps.
func (s *StaticHTMLStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{"python3", "-m", "http.server", itoa(port), "--directory", workDir}
}

func (s *StaticHTMLStrategy) BaseImage() string { return "python:3.12-slim" }

func (s *StaticHTMLStrategy) NeedsRestart() bool { return false }

func (s *StaticHTMLStrategy) DepsRequireRebuild(files []string) bool { return false }

// PythonFlaskStrategy runs Flask with --reload (Werkzeug reloader).
// Matched when language is "python" and framework is "flask" or empty.
type PythonFlaskStrategy struct{}

func (s *PythonFlaskStrategy) Name() string { return "python-flask" }

func (s *PythonFlaskStrategy) Matches(language, framework string, files []string) bool {
	return language == "python" && (framework == "flask" || framework == "")
}

func (s *PythonFlaskStrategy) WatchPatterns() []string {
	return []string{"*.py", "*.html", "*.css", "*.js", "*.jinja", "*.jinja2"}
}

func (s *PythonFlaskStrategy) IgnorePatterns() []string {
	return []string{".git", "__pycache__", "*.pyc", "*.pyo", ".venv", "venv", "node_modules"}
}

func (s *PythonFlaskStrategy) DevCommand(workDir string, port int, env []string) []string {
	// Install Flask quietly if absent, then start with the Werkzeug reloader.
	// FLASK_DEBUG=1 enables the reloader without exposing the debugger PIN
	// (the sandbox network firewall handles external access).
	return []string{
		"sh", "-c",
		"pip install -q flask 2>/dev/null; " +
			"FLASK_APP=" + workDir + " FLASK_ENV=development FLASK_DEBUG=1 " +
			"python -m flask run --host=0.0.0.0 --port=" + itoa(port),
	}
}

func (s *PythonFlaskStrategy) BaseImage() string { return "python:3.12-slim" }

func (s *PythonFlaskStrategy) NeedsRestart() bool { return false }

func (s *PythonFlaskStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "requirements.txt", "requirements.in", "pyproject.toml", "setup.py", "setup.cfg")
}

// PythonUvicornStrategy runs Uvicorn with --reload for ASGI apps (FastAPI,
// Starlette, etc.).
type PythonUvicornStrategy struct{}

func (s *PythonUvicornStrategy) Name() string { return "python-uvicorn" }

func (s *PythonUvicornStrategy) Matches(language, framework string, files []string) bool {
	return language == "python" && (framework == "fastapi" || framework == "starlette" || framework == "uvicorn")
}

func (s *PythonUvicornStrategy) WatchPatterns() []string {
	return []string{"*.py", "*.html", "*.css", "*.js"}
}

func (s *PythonUvicornStrategy) IgnorePatterns() []string {
	return []string{".git", "__pycache__", "*.pyc", "*.pyo", ".venv", "venv"}
}

func (s *PythonUvicornStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{
		"sh", "-c",
		"pip install -q uvicorn 2>/dev/null; " +
			"cd " + workDir + " && uvicorn main:app --host 0.0.0.0 --port " + itoa(port) + " --reload",
	}
}

func (s *PythonUvicornStrategy) BaseImage() string { return "python:3.12-slim" }

func (s *PythonUvicornStrategy) NeedsRestart() bool { return false }

func (s *PythonUvicornStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "requirements.txt", "requirements.in", "pyproject.toml", "setup.py")
}

// PythonDjangoStrategy runs Django's development server via manage.py runserver.
// Django's autoreloader detects .py changes automatically.
type PythonDjangoStrategy struct{}

func (s *PythonDjangoStrategy) Name() string { return "python-django" }

func (s *PythonDjangoStrategy) Matches(language, framework string, files []string) bool {
	return language == "python" && framework == "django"
}

func (s *PythonDjangoStrategy) WatchPatterns() []string {
	return []string{"*.py", "*.html", "*.css", "*.js"}
}

func (s *PythonDjangoStrategy) IgnorePatterns() []string {
	return []string{".git", "__pycache__", "*.pyc", "*.pyo", ".venv", "venv"}
}

func (s *PythonDjangoStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{
		"sh", "-c",
		"cd " + workDir + " && python manage.py runserver 0.0.0.0:" + itoa(port),
	}
}

func (s *PythonDjangoStrategy) BaseImage() string { return "python:3.12-slim" }

func (s *PythonDjangoStrategy) NeedsRestart() bool { return false }

func (s *PythonDjangoStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "requirements.txt", "pyproject.toml", "setup.py")
}

// PythonGenericStrategy runs any Python script. It cannot hot-reload because
// there is no framework watcher; the platform must restart the process.
//
// IMPORTANT: do not advertise generic Python scripts as hot-reloadable. The
// NeedsRestart() method returns true to signal this correctly.
type PythonGenericStrategy struct{}

func (s *PythonGenericStrategy) Name() string { return "python-generic" }

func (s *PythonGenericStrategy) Matches(language, framework string, files []string) bool {
	return language == "python"
}

func (s *PythonGenericStrategy) WatchPatterns() []string { return []string{"*.py"} }

func (s *PythonGenericStrategy) IgnorePatterns() []string {
	return []string{".git", "__pycache__", "*.pyc", "*.pyo", ".venv", "venv"}
}

func (s *PythonGenericStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{"sh", "-c", "cd " + workDir + " && python main.py"}
}

func (s *PythonGenericStrategy) BaseImage() string { return "python:3.12-slim" }

// NeedsRestart is true: the platform must restart the process after each save.
func (s *PythonGenericStrategy) NeedsRestart() bool { return true }

func (s *PythonGenericStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "requirements.txt", "pyproject.toml", "setup.py")
}

// NextJSStrategy uses Next.js built-in HMR via `npm run dev`. Webpack and
// Turbopack both respond to file changes without restarting the process.
type NextJSStrategy struct{}

func (s *NextJSStrategy) Name() string { return "nextjs" }

func (s *NextJSStrategy) Matches(language, framework string, files []string) bool {
	return framework == "next" || framework == "nextjs" || framework == "next.js"
}

func (s *NextJSStrategy) WatchPatterns() []string {
	return []string{"*.ts", "*.tsx", "*.js", "*.jsx", "*.css", "*.json"}
}

func (s *NextJSStrategy) IgnorePatterns() []string {
	return []string{".git", "node_modules", ".next", "out", "dist"}
}

func (s *NextJSStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{
		"sh", "-c",
		"cd " + workDir + " && npm install -s 2>/dev/null; npm run dev -- --port " + itoa(port),
	}
}

func (s *NextJSStrategy) BaseImage() string { return "node:22-slim" }

func (s *NextJSStrategy) NeedsRestart() bool { return false }

func (s *NextJSStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml")
}

// ReactStrategy covers Vite and Create React App dev servers. Both support HMR.
type ReactStrategy struct{}

func (s *ReactStrategy) Name() string { return "react" }

func (s *ReactStrategy) Matches(language, framework string, files []string) bool {
	return framework == "react" || framework == "vite"
}

func (s *ReactStrategy) WatchPatterns() []string {
	return []string{"*.ts", "*.tsx", "*.js", "*.jsx", "*.css"}
}

func (s *ReactStrategy) IgnorePatterns() []string {
	return []string{".git", "node_modules", "dist", "build"}
}

func (s *ReactStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{
		"sh", "-c",
		"cd " + workDir + " && npm install -s 2>/dev/null; npm run dev -- --host 0.0.0.0 --port " + itoa(port),
	}
}

func (s *ReactStrategy) BaseImage() string { return "node:22-slim" }

func (s *ReactStrategy) NeedsRestart() bool { return false }

func (s *ReactStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml")
}

// NodeWatchStrategy uses Node.js's built-in --watch flag for plain Node apps.
// If the project has an npm script it defers to that instead.
type NodeWatchStrategy struct{}

func (s *NodeWatchStrategy) Name() string { return "node-watch" }

func (s *NodeWatchStrategy) Matches(language, framework string, files []string) bool {
	return language == "node" || language == "javascript" || language == "typescript"
}

func (s *NodeWatchStrategy) WatchPatterns() []string {
	return []string{"*.js", "*.mjs", "*.cjs", "*.ts", "*.json"}
}

func (s *NodeWatchStrategy) IgnorePatterns() []string {
	return []string{".git", "node_modules", "dist", "build", ".next"}
}

func (s *NodeWatchStrategy) DevCommand(workDir string, port int, env []string) []string {
	// Prefer npm scripts (dev, then start), fall back to node --watch on the
	// detected entry file, then on the whole directory.
	return []string{
		"sh", "-c",
		"cd " + workDir + " && npm install -s 2>/dev/null; " +
			"if [ -f package.json ] && node -e " +
			`'process.exit(require("./package.json").scripts&&require("./package.json").scripts.dev?0:1)' ` +
			"2>/dev/null; then npm run dev; " +
			"elif [ -f package.json ] && node -e " +
			`'process.exit(require("./package.json").scripts&&require("./package.json").scripts.start?0:1)' ` +
			"2>/dev/null; then npm start; " +
			"elif [ -f index.js ]; then node --watch index.js; " +
			"elif [ -f server.js ]; then node --watch server.js; " +
			"else node --watch .; fi",
	}
}

func (s *NodeWatchStrategy) BaseImage() string { return "node:22-slim" }

func (s *NodeWatchStrategy) NeedsRestart() bool { return false }

func (s *NodeWatchStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml")
}

// GoCompileStrategy compiles and runs a Go binary with `go run .`.  Source
// changes require a process restart; the platform signals SIGTERM and
// re-launches the command inside the container.
type GoCompileStrategy struct{}

func (s *GoCompileStrategy) Name() string { return "go-compile" }

func (s *GoCompileStrategy) Matches(language, framework string, files []string) bool {
	return language == "go"
}

func (s *GoCompileStrategy) WatchPatterns() []string { return []string{"*.go"} }

func (s *GoCompileStrategy) IgnorePatterns() []string {
	return []string{".git", "vendor", "*_test.go"}
}

func (s *GoCompileStrategy) DevCommand(workDir string, port int, env []string) []string {
	// `go run .` compiles and runs in one step; the Go toolchain is present
	// in the golang:* base image.
	return []string{"sh", "-c", "cd " + workDir + " && PORT=" + itoa(port) + " go run ."}
}

func (s *GoCompileStrategy) BaseImage() string { return "golang:1.24-bookworm" }

// NeedsRestart is true: the platform must kill and re-exec `go run .` after a
// source change, because there is no framework-level hot-reload.
func (s *GoCompileStrategy) NeedsRestart() bool { return true }

func (s *GoCompileStrategy) DepsRequireRebuild(files []string) bool {
	return hasFile(files, "go.mod", "go.sum")
}

// CCppStrategy compiles and starts a C/C++ binary. Compilation happens inside
// the dev container via exec; the platform restarts the process after each
// successful build.
type CCppStrategy struct{}

func (s *CCppStrategy) Name() string { return "c-cpp" }

func (s *CCppStrategy) Matches(language, framework string, files []string) bool {
	return language == "c" || language == "c++" || language == "cpp"
}

func (s *CCppStrategy) WatchPatterns() []string {
	return []string{"*.c", "*.cpp", "*.h", "*.hpp", "*.cc", "Makefile"}
}

func (s *CCppStrategy) IgnorePatterns() []string {
	return []string{".git", "*.o", "*.a", "*.so"}
}

func (s *CCppStrategy) DevCommand(workDir string, port int, env []string) []string {
	return []string{
		"sh", "-c",
		"cd " + workDir + "; " +
			"if [ -f Makefile ]; then make && ./a.out; " +
			"elif ls *.c 2>/dev/null | head -1 | grep -q .; then gcc -o /tmp/app_dev *.c && PORT=" + itoa(port) + " /tmp/app_dev; " +
			"elif ls *.cpp 2>/dev/null | head -1 | grep -q .; then g++ -o /tmp/app_dev *.cpp && PORT=" + itoa(port) + " /tmp/app_dev; " +
			"else echo 'no C/C++ source found'; sleep 30; fi",
	}
}

func (s *CCppStrategy) BaseImage() string { return "gcc:14" }

// NeedsRestart is true: the platform must kill and re-exec after each save.
func (s *CCppStrategy) NeedsRestart() bool { return true }

func (s *CCppStrategy) DepsRequireRebuild(files []string) bool { return false }
