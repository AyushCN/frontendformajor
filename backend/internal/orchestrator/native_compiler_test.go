package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCompiler_CompileC_Success(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found, skipping C native compilation test")
	}

	repoDir := t.TempDir()
	buildDir := t.TempDir()

	srcFile := filepath.Join(repoDir, "temp.c")
	code := `
#include <stdio.h>
int main() {
    printf("Hello from temp.c\\n");
    return 0;
}
`
	if err := os.WriteFile(srcFile, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}

	compiler := NewNativeCompiler()
	res, err := compiler.Compile(context.Background(), CompileRequest{
		SourceFile: srcFile,
		Language:   "c",
		RepoDir:    repoDir,
		BuildDir:   buildDir,
	})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected successful compilation, got failure: %s\n%s", res.Error, res.Output)
	}

	if res.BinaryPath == "" {
		t.Fatal("expected non-empty BinaryPath")
	}

	// Verify binary was written to buildDir and NOT to repoDir
	if !strings.HasPrefix(res.BinaryPath, buildDir) {
		t.Errorf("binary path %q is not inside buildDir %q", res.BinaryPath, buildDir)
	}

	// Verify repoDir contains only the original temp.c
	entries, _ := os.ReadDir(repoDir)
	if len(entries) != 1 || entries[0].Name() != "temp.c" {
		t.Errorf("original repo directory was polluted: %+v", entries)
	}

	// Verify the binary can execute
	out, execErr := exec.Command(res.BinaryPath).CombinedOutput()
	if execErr != nil {
		t.Fatalf("execution failed: %v", execErr)
	}
	if !strings.Contains(string(out), "Hello from temp.c") {
		t.Errorf("unexpected output: %s", string(out))
	}
}

func TestNativeCompiler_CompileCpp_Success(t *testing.T) {
	if _, err := exec.LookPath("g++"); err != nil {
		t.Skip("g++ not found, skipping C++ native compilation test")
	}

	repoDir := t.TempDir()
	buildDir := t.TempDir()

	srcFile := filepath.Join(repoDir, "nold.cpp")
	code := `
#include <iostream>
int main() {
    std::cout << "Hello from nold.cpp" << std::endl;
    return 0;
}
`
	if err := os.WriteFile(srcFile, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}

	compiler := NewNativeCompiler()
	res, err := compiler.Compile(context.Background(), CompileRequest{
		SourceFile: srcFile,
		Language:   "cpp",
		RepoDir:    repoDir,
		BuildDir:   buildDir,
	})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected successful compilation, got failure: %s\n%s", res.Error, res.Output)
	}

	// Execute binary
	out, execErr := exec.Command(res.BinaryPath).CombinedOutput()
	if execErr != nil {
		t.Fatalf("execution failed: %v", execErr)
	}
	if !strings.Contains(string(out), "Hello from nold.cpp") {
		t.Errorf("unexpected output: %s", string(out))
	}
}

func TestNativeCompiler_CompilationFailure_CapturedOutput(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found, skipping C native compilation test")
	}

	repoDir := t.TempDir()
	buildDir := t.TempDir()

	srcFile := filepath.Join(repoDir, "broken.c")
	code := `
#include <stdio.h>
int main() {
    this_is_a_syntax_error_undefined_symbol();
    return 0;
}
`
	if err := os.WriteFile(srcFile, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}

	compiler := NewNativeCompiler()
	res, err := compiler.Compile(context.Background(), CompileRequest{
		SourceFile: srcFile,
		Language:   "c",
		RepoDir:    repoDir,
		BuildDir:   buildDir,
	})
	if err != nil {
		t.Fatalf("Compile unexpectedly returned Go error: %v", err)
	}

	if res.Success {
		t.Fatal("expected compilation to fail for syntax error")
	}

	if !strings.Contains(res.Output, "this_is_a_syntax_error") && !strings.Contains(res.Error, "this_is_a_syntax_error") {
		t.Errorf("expected compiler error message to capture syntax error, got output: %q", res.Output)
	}
}

func TestNativeCompiler_MissingCompiler(t *testing.T) {
	compiler := NewNativeCompiler()
	compiler.SetCompilers("", "", "") // Simulate environment with no compilers installed

	repoDir := t.TempDir()
	buildDir := t.TempDir()
	srcFile := filepath.Join(repoDir, "test.c")
	_ = os.WriteFile(srcFile, []byte("int main(){return 0;}"), 0644)

	res, err := compiler.Compile(context.Background(), CompileRequest{
		SourceFile: srcFile,
		Language:   "c",
		RepoDir:    repoDir,
		BuildDir:   buildDir,
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}

	if res.Success {
		t.Fatal("should not succeed without compiler")
	}
	if !strings.Contains(res.Error, "missing C compiler") {
		t.Errorf("expected missing compiler error, got %q", res.Error)
	}
}

func TestNativeCompiler_RejectsBuildDirInsideRepo(t *testing.T) {
	compiler := NewNativeCompiler()
	repoDir := t.TempDir()
	buildDir := filepath.Join(repoDir, "inside_build")

	srcFile := filepath.Join(repoDir, "test.c")
	_ = os.WriteFile(srcFile, []byte("int main(){return 0;}"), 0644)

	_, err := compiler.Compile(context.Background(), CompileRequest{
		SourceFile: srcFile,
		Language:   "c",
		RepoDir:    repoDir,
		BuildDir:   buildDir,
	})

	if err == nil {
		t.Fatal("expected error when BuildDir is inside RepoDir, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be inside user repository") {
		t.Errorf("expected repository boundary error, got %v", err)
	}
}
