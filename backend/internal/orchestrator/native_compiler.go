package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CompileRequest defines parameters for a native code compilation.
type CompileRequest struct {
	SourceFile  string // Path to source file
	Language    string // "c", "cpp", "rust"
	RepoDir     string // Working directory / root of repository
	BuildDir    string // Controlled temporary build directory (MUST NOT be inside RepoDir)
	ExtraFlags  []string
	TimeoutSecs int
}

// CompileResult captures the outcome and telemetry of native compilation.
type CompileResult struct {
	Success      bool   `json:"success"`
	BinaryPath   string `json:"binary_path,omitempty"`
	CompilerUsed string `json:"compiler_used,omitempty"`
	Output       string `json:"output,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

// NativeCompiler manages toolchain discovery and isolated compilation for C, C++, and Rust.
type NativeCompiler struct {
	cCompiler    string
	cppCompiler  string
	rustCompiler string
}

// NewNativeCompiler initializes compiler paths by checking toolchain availability.
func NewNativeCompiler() *NativeCompiler {
	cComp, _ := exec.LookPath("gcc")
	if cComp == "" {
		cComp, _ = exec.LookPath("clang")
	}

	cppComp, _ := exec.LookPath("g++")
	if cppComp == "" {
		cppComp, _ = exec.LookPath("clang++")
	}

	rustComp, _ := exec.LookPath("rustc")

	return &NativeCompiler{
		cCompiler:    cComp,
		cppCompiler:  cppComp,
		rustCompiler: rustComp,
	}
}

// SetCompilers allows overriding compiler binaries (useful for hermetic tests or container runtimes).
func (nc *NativeCompiler) SetCompilers(c, cpp, rust string) {
	nc.cCompiler = c
	nc.cppCompiler = cpp
	nc.rustCompiler = rust
}

// HasCompiler returns true if a valid compiler is available for the given language.
func (nc *NativeCompiler) HasCompiler(lang string) bool {
	switch strings.ToLower(lang) {
	case "c":
		return nc.cCompiler != ""
	case "cpp":
		return nc.cppCompiler != ""
	case "rust":
		return nc.rustCompiler != ""
	default:
		return false
	}
}

// Compile executes native compilation in an isolated build directory outside the user's repository.
func (nc *NativeCompiler) Compile(ctx context.Context, req CompileRequest) (*CompileResult, error) {
	start := time.Now()

	// Validate SourceFile
	srcPath := req.SourceFile
	if !filepath.IsAbs(srcPath) && req.RepoDir != "" {
		srcPath = filepath.Join(req.RepoDir, srcPath)
	}

	if _, err := os.Stat(srcPath); err != nil {
		return &CompileResult{
			Success: false,
			Error:   fmt.Sprintf("source file %q not found: %v", req.SourceFile, err),
		}, nil
	}

	// Validate BuildDir
	if req.BuildDir == "" {
		return nil, fmt.Errorf("native compiler: BuildDir must not be empty")
	}

	// Invariant: Do not write generated binaries into user's source repository
	if req.RepoDir != "" {
		cleanRepo := filepath.Clean(req.RepoDir)
		cleanBuild := filepath.Clean(req.BuildDir)
		if cleanBuild == cleanRepo || strings.HasPrefix(cleanBuild+string(os.PathSeparator), cleanRepo+string(os.PathSeparator)) {
			return nil, fmt.Errorf("native compiler: BuildDir %q cannot be inside user repository %q", req.BuildDir, req.RepoDir)
		}
	}

	if err := os.MkdirAll(req.BuildDir, 0755); err != nil {
		return nil, fmt.Errorf("native compiler: cannot create build dir %q: %w", req.BuildDir, err)
	}

	// Resolve compiler and arguments
	var compiler string
	var args []string

	baseName := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	binName := baseName + "_bin"
	outPath := filepath.Join(req.BuildDir, binName)

	switch strings.ToLower(req.Language) {
	case "c":
		if nc.cCompiler == "" {
			return &CompileResult{
				Success: false,
				Error:   "missing C compiler: neither gcc nor clang is installed in the execution environment",
			}, nil
		}
		compiler = nc.cCompiler
		args = append(args, "-O2", "-Wall", "-o", outPath, srcPath)
	case "cpp":
		if nc.cppCompiler == "" {
			return &CompileResult{
				Success: false,
				Error:   "missing C++ compiler: neither g++ nor clang++ is installed in the execution environment",
			}, nil
		}
		compiler = nc.cppCompiler
		args = append(args, "-O2", "-Wall", "-std=c++17", "-o", outPath, srcPath)
	case "rust":
		if nc.rustCompiler == "" {
			return &CompileResult{
				Success: false,
				Error:   "missing Rust compiler: rustc is not installed in the execution environment",
			}, nil
		}
		compiler = nc.rustCompiler
		args = append(args, "-O", "-o", outPath, srcPath)
	default:
		return nil, fmt.Errorf("native compiler: unsupported compiled language %q", req.Language)
	}

	if len(req.ExtraFlags) > 0 {
		args = append(args, req.ExtraFlags...)
	}

	timeout := 30 * time.Second
	if req.TimeoutSecs > 0 {
		timeout = time.Duration(req.TimeoutSecs) * time.Second
	}

	compileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(compileCtx, compiler, args...)
	cmd.Dir = req.RepoDir

	var outputBuf bytes.Buffer
	cmd.Stdout = &outputBuf
	cmd.Stderr = &outputBuf

	err := cmd.Run()
	durationMs := time.Since(start).Milliseconds()
	outputStr := strings.TrimSpace(outputBuf.String())

	if compileCtx.Err() == context.DeadlineExceeded {
		return &CompileResult{
			Success:      false,
			CompilerUsed: compiler,
			Output:       outputStr,
			DurationMs:   durationMs,
			Error:        fmt.Sprintf("compilation timed out after %v", timeout),
		}, nil
	}

	if err != nil {
		return &CompileResult{
			Success:      false,
			CompilerUsed: compiler,
			Output:       outputStr,
			DurationMs:   durationMs,
			Error:        fmt.Sprintf("compilation failed: %v\n%s", err, outputStr),
		}, nil
	}

	// Verify binary was actually created and is executable
	info, statErr := os.Stat(outPath)
	if statErr != nil || info.IsDir() {
		return &CompileResult{
			Success:      false,
			CompilerUsed: compiler,
			Output:       outputStr,
			DurationMs:   durationMs,
			Error:        "compiler exited successfully but output binary was not found",
		}, nil
	}

	return &CompileResult{
		Success:      true,
		BinaryPath:   outPath,
		CompilerUsed: compiler,
		Output:       outputStr,
		DurationMs:   durationMs,
	}, nil
}
