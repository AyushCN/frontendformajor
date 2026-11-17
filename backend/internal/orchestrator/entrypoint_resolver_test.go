package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to create a repository fixture for entrypoint tests
func createTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	return dir
}

// ----------------------------------------------------------------------------
// Python Tests (Arbitrary Filenames)
// ----------------------------------------------------------------------------

func TestEntryPointResolver_ArbitraryPythonFilename_GPy(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"g.py": "print('hello from g.py')\n",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution for single g.py, got ambiguous")
	}
	if res.SelectedCommand != "python g.py" {
		t.Errorf("expected 'python g.py', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected SCRIPT, got %s", res.ExecutionMode)
	}
	if res.CommandSource != "SINGLE_FILE_INFERENCE" {
		t.Errorf("expected SINGLE_FILE_INFERENCE, got %s", res.CommandSource)
	}
}

func TestEntryPointResolver_ArbitraryPythonFilename_TempPy(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"temp.py": "x = 42\nprint(x * 2)\n",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "python temp.py" {
		t.Errorf("expected 'python temp.py', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected SCRIPT, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_InteractiveScript_Input(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"calculator.py": `
n = int(input("Enter a number: "))
print("Factorial is:", n * (n - 1))
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeInteractiveScript {
		t.Errorf("expected ExecutionModeInteractiveScript, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "python calculator.py" {
		t.Errorf("expected 'python calculator.py', got %q", res.SelectedCommand)
	}
	if res.PrimaryCandidate == nil || !res.PrimaryCandidate.HasInteractive {
		t.Errorf("expected HasInteractive=true, got %+v", res.PrimaryCandidate)
	}
}

func TestEntryPointResolver_FlaskApplication(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"server.py": `
from flask import Flask
app = Flask(__name__)

@app.route("/")
def home():
    return "Hello Flask"
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "python server.py" {
		t.Errorf("expected 'python server.py', got %q", res.SelectedCommand)
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.Framework != "Flask" {
		t.Errorf("expected Framework Flask, got %+v", res.PrimaryCandidate)
	}
}

// ----------------------------------------------------------------------------
// C Tests (Arbitrary Filenames & Compilation)
// ----------------------------------------------------------------------------

func TestEntryPointResolver_ArbitraryCFilename_TempC(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"temp.c": `
#include <stdio.h>
int main() {
    printf("factorial\n");
    return 0;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution for single temp.c")
	}
	if res.SelectedCommand != "./temp_bin" {
		t.Errorf("expected './temp_bin', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected SCRIPT, got %s", res.ExecutionMode)
	}
	if res.PrimaryCandidate == nil || !res.PrimaryCandidate.RequiresCompile {
		t.Errorf("expected RequiresCompile=true, got %+v", res.PrimaryCandidate)
	}
	if !strings.Contains(res.BuildCommand, "gcc") && !strings.Contains(res.BuildCommand, "clang") {
		t.Errorf("expected gcc/clang compile command, got %q", res.BuildCommand)
	}
}

func TestEntryPointResolver_InteractiveCProgram(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"calc.c": `
#include <stdio.h>
int main() {
    int n;
    printf("Enter number: ");
    scanf("%d", &n);
    printf("Result: %d\n", n * n);
    return 0;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeInteractiveScript {
		t.Errorf("expected ExecutionModeInteractiveScript for scanf, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "./calc_bin" {
		t.Errorf("expected './calc_bin', got %q", res.SelectedCommand)
	}
}

func TestEntryPointResolver_CWithHelperModule(t *testing.T) {
	// main.c has main(), helper.c does not have main()
	repo := createTestRepo(t, map[string]string{
		"main.c": `
#include <stdio.h>
int add(int, int);
int main() {
    printf("%d\n", add(1, 2));
    return 0;
}
`,
		"helper.c": `
int add(int a, int b) {
    return a + b;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution (main.c has main, helper.c does not)")
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.RelativePath != "main.c" {
		t.Errorf("expected main.c as primary, got %+v", res.PrimaryCandidate)
	}
}

func TestEntryPointResolver_AmbiguousCFiles(t *testing.T) {
	// Both temp.c and program.c have main() -> Ambiguous!
	repo := createTestRepo(t, map[string]string{
		"temp.c":    "int main() { return 0; }\n",
		"program.c": "int main() { return 0; }\n",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if !res.IsAmbiguous {
		t.Errorf("expected AMBIGUOUS when multiple C files define main()")
	}
	if res.ExecutionMode != ExecutionModeAmbiguous {
		t.Errorf("expected ExecutionModeAmbiguous, got %s", res.ExecutionMode)
	}
	if res.CommandSource != "AMBIGUOUS" {
		t.Errorf("expected AMBIGUOUS source, got %s", res.CommandSource)
	}
}

// ----------------------------------------------------------------------------
// C++ Tests (Arbitrary Filenames)
// ----------------------------------------------------------------------------

func TestEntryPointResolver_ArbitraryCppFilename_NoldCpp(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"nold.cpp": `
#include <iostream>
int main() {
    std::cout << "factorial calculation" << std::endl;
    return 0;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution for single nold.cpp")
	}
	if res.SelectedCommand != "./nold_bin" {
		t.Errorf("expected './nold_bin', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected SCRIPT, got %s", res.ExecutionMode)
	}
	if res.PrimaryCandidate == nil || !res.PrimaryCandidate.RequiresCompile {
		t.Errorf("expected RequiresCompile=true, got %+v", res.PrimaryCandidate)
	}
	if !strings.Contains(res.BuildCommand, "g++") && !strings.Contains(res.BuildCommand, "clang++") {
		t.Errorf("expected g++/clang++ compile command, got %q", res.BuildCommand)
	}
}

func TestEntryPointResolver_InteractiveCppProgram(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"interactive.cpp": `
#include <iostream>
int main() {
    int x;
    std::cin >> x;
    std::cout << x * 2 << std::endl;
    return 0;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeInteractiveScript {
		t.Errorf("expected ExecutionModeInteractiveScript for std::cin, got %s", res.ExecutionMode)
	}
}

// ----------------------------------------------------------------------------
// Mixed-Language Repositories
// ----------------------------------------------------------------------------

func TestEntryPointResolver_MixedLanguage_PythonAndC(t *testing.T) {
	// A mixed repo with a python runner script and a C library without main
	repo := createTestRepo(t, map[string]string{
		"run.py": `
import ctypes
print("Running python orchestrator")
`,
		"fast_math.c": `
int multiply(int a, int b) {
    return a * b;
}
`,
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution (run.py is executable, fast_math.c has no main)")
	}
	if res.SelectedCommand != "python run.py" {
		t.Errorf("expected 'python run.py', got %q", res.SelectedCommand)
	}

	// Verify mixed language detection warning is recorded
	foundMixedWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "mixed-language repository") {
			foundMixedWarning = true
			break
		}
	}
	if !foundMixedWarning {
		t.Errorf("expected mixed-language warning, warnings were: %+v", res.Warnings)
	}
}

// ----------------------------------------------------------------------------
// Existing Supported Languages: Node.js, Go, Java
// ----------------------------------------------------------------------------

func TestEntryPointResolver_NodeJs_PackageJSON(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"package.json": `{"name":"test","scripts":{"start":"node index.js"}}`,
		"index.js":     "console.log('hi')",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "npm start" {
		t.Errorf("expected 'npm start', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_Golang_MainGo(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello Go\")\n}\n",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "go run main.go" && res.SelectedCommand != "go run ." {
		t.Errorf("expected 'go run main.go' or 'go run .', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected ExecutionModeScript, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_Java_PomXML(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"pom.xml": "<project><modelVersion>4.0.0</modelVersion></project>",
	})

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.PrimaryCandidate == nil || res.PrimaryCandidate.Language != "java" {
		t.Errorf("expected Java candidate, got %+v", res.PrimaryCandidate)
	}
}

// ----------------------------------------------------------------------------
// Explicit User Override & Safety
// ----------------------------------------------------------------------------

func TestEntryPointResolver_ExplicitUserCommandOverride(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"temp.c": "int main(){return 0;}",
	})

	resolver := NewEntryPointResolver()
	userCfg := &UserBuildConfig{
		StartCmd: "make run_custom",
	}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, userCfg)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "make run_custom" {
		t.Errorf("expected user explicit command, got %q", res.SelectedCommand)
	}
	if res.CommandSource != "USER_EXPLICIT" {
		t.Errorf("expected USER_EXPLICIT, got %s", res.CommandSource)
	}
}

func TestEntryPointResolver_SymlinkEscape_Rejected(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "malicious.py")
	if err := os.WriteFile(outsideFile, []byte("print('pwned')"), 0644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(repo, "link.py")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	for _, c := range res.Candidates {
		if c.RelativePath == "link.py" {
			t.Errorf("escaping symlink should not be an entry point candidate")
		}
	}
}

func TestEntryPointResolver_OriginalRepoImmutable(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"temp.c":   "int main(){return 0;}",
		"nold.cpp": "int main(){return 0;}",
	})

	beforeEntries, _ := os.ReadDir(repo)

	resolver := NewEntryPointResolver()
	_, err := resolver.ResolveEntryPoints(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	afterEntries, _ := os.ReadDir(repo)
	if len(beforeEntries) != len(afterEntries) {
		t.Errorf("repository contents changed during analysis: before %d, after %d",
			len(beforeEntries), len(afterEntries))
	}
}
