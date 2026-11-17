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

func TestEntryPointResolver_SinglePythonFile_APy(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"a.py": "print(10 + 20)\n",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution for single a.py, got ambiguous")
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected ExecutionModeScript, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "python a.py" {
		t.Errorf("expected 'python a.py', got %q", res.SelectedCommand)
	}
	if res.CommandSource != "SINGLE_FILE_INFERENCE" {
		t.Errorf("expected command source SINGLE_FILE_INFERENCE, got %s", res.CommandSource)
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.RelativePath != "a.py" {
		t.Errorf("expected primary candidate a.py, got %+v", res.PrimaryCandidate)
	}
	if res.Confidence != ConfidenceHigh {
		t.Errorf("expected ConfidenceHigh, got %s", res.Confidence)
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
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
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
		"app.py": `
from flask import Flask
app = Flask(__name__)

@app.route("/")
def home():
    return "Hello Flask"

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)
`,
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "python app.py" {
		t.Errorf("expected 'python app.py', got %q", res.SelectedCommand)
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.Framework != "Flask" {
		t.Errorf("expected Framework Flask, got %+v", res.PrimaryCandidate)
	}
}

func TestEntryPointResolver_FastAPIApplication(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.py": `
from fastapi import FastAPI
app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "Hello FastAPI"}
`,
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
	if res.SelectedCommand != "python main.py" {
		t.Errorf("expected 'python main.py', got %q", res.SelectedCommand)
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.Framework != "FastAPI" {
		t.Errorf("expected Framework FastAPI, got %+v", res.PrimaryCandidate)
	}
}

func TestEntryPointResolver_ConventionalFilenameRank(t *testing.T) {
	// main.py should beat helper.py easily
	repo := createTestRepo(t, map[string]string{
		"main.py":   "import helper\nprint(helper.calc())\n",
		"helper.py": "def calc(): return 42\n",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.IsAmbiguous {
		t.Errorf("expected unambiguous resolution when main.py is present")
	}
	if res.PrimaryCandidate == nil || res.PrimaryCandidate.RelativePath != "main.py" {
		t.Errorf("expected primary candidate main.py, got %+v", res.PrimaryCandidate)
	}
	if res.SelectedCommand != "python main.py" {
		t.Errorf("expected 'python main.py', got %q", res.SelectedCommand)
	}
	if res.CommandSource != "CONVENTIONAL_FILENAME" {
		t.Errorf("expected CONVENTIONAL_FILENAME, got %s", res.CommandSource)
	}
}

func TestEntryPointResolver_MultipleAmbiguousPythonFiles(t *testing.T) {
	// Equal ranking scripts: a.py, b.py, c.py
	repo := createTestRepo(t, map[string]string{
		"a.py": "print('alpha')\n",
		"b.py": "print('beta')\n",
		"c.py": "print('gamma')\n",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if !res.IsAmbiguous {
		t.Errorf("expected ambiguous resolution for multiple equal scripts")
	}
	if res.ExecutionMode != ExecutionModeAmbiguous {
		t.Errorf("expected ExecutionModeAmbiguous, got %s", res.ExecutionMode)
	}
	if res.CommandSource != "AMBIGUOUS" {
		t.Errorf("expected command source AMBIGUOUS, got %s", res.CommandSource)
	}
	if len(res.Candidates) != 3 {
		t.Errorf("expected 3 candidates, got %d", len(res.Candidates))
	}
}

func TestEntryPointResolver_ExplicitUserCommandOverride(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"app.py": "from flask import Flask\n",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}
	userCfg := &UserBuildConfig{
		StartCmd: "python -m custom.worker --threads=4",
	}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, userCfg)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "python -m custom.worker --threads=4" {
		t.Errorf("expected explicit command, got %q", res.SelectedCommand)
	}
	if res.CommandSource != "USER_EXPLICIT" {
		t.Errorf("expected USER_EXPLICIT, got %s", res.CommandSource)
	}
	if res.Confidence != ConfidenceHigh {
		t.Errorf("expected ConfidenceHigh, got %s", res.Confidence)
	}
}

func TestEntryPointResolver_ProjectConfig_RailpackJSON(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"railpack.json": `{"deploy": {"startCommand": "gunicorn server:app"}}`,
		"server.py":     "print('server')",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "gunicorn server:app" {
		t.Errorf("expected 'gunicorn server:app', got %q", res.SelectedCommand)
	}
	if res.CommandSource != "PROJECT_CONFIG" {
		t.Errorf("expected PROJECT_CONFIG, got %s", res.CommandSource)
	}
	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_ProjectConfig_Procfile(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"Procfile":  "web: uvicorn app.api:app --host 0.0.0.0 --port 5000\n",
		"server.py": "print('server')",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "python"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "uvicorn app.api:app --host 0.0.0.0 --port 5000" {
		t.Errorf("expected Procfile command, got %q", res.SelectedCommand)
	}
	if res.CommandSource != "PROJECT_CONFIG" {
		t.Errorf("expected PROJECT_CONFIG, got %s", res.CommandSource)
	}
}

func TestEntryPointResolver_NodeJs_PackageJSON(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"package.json": `{"name":"test","scripts":{"start":"node index.js"}}`,
		"index.js":     "console.log('hi')",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "nodejs"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
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

func TestEntryPointResolver_NodeJs_SingleScript(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"script.js": "console.log('standalone script');",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "nodejs"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "node script.js" {
		t.Errorf("expected 'node script.js', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected ExecutionModeScript, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_Golang_MainGo(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello Go\")\n}\n",
	})

	resolver := NewEntryPointResolver()
	analysis := &RepositoryAnalysis{PrimaryLanguage: "golang"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "go run ." {
		t.Errorf("expected 'go run .', got %q", res.SelectedCommand)
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
	analysis := &RepositoryAnalysis{PrimaryLanguage: "java"}

	res, err := resolver.ResolveEntryPoints(context.Background(), repo, analysis, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	if res.SelectedCommand != "mvn spring-boot:run" {
		t.Errorf("expected 'mvn spring-boot:run', got %q", res.SelectedCommand)
	}
	if res.ExecutionMode != ExecutionModeApplication {
		t.Errorf("expected ExecutionModeApplication, got %s", res.ExecutionMode)
	}
}

func TestEntryPointResolver_SymlinkEscape_Rejected(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "malicious.py")
	if err := os.WriteFile(outsideFile, []byte("print('pwned')"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink pointing outside
	linkPath := filepath.Join(repo, "link.py")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	resolver := NewEntryPointResolver()
	res, err := resolver.ResolveEntryPoints(context.Background(), repo, &RepositoryAnalysis{PrimaryLanguage: "python"}, nil)
	if err != nil {
		t.Fatalf("ResolveEntryPoints error: %v", err)
	}

	// link.py must NOT be accepted as a candidate
	for _, c := range res.Candidates {
		if c.RelativePath == "link.py" {
			t.Errorf("symlink escaping repo root should not be an entry point candidate")
		}
	}
}

func TestEntryPointResolver_OriginalRepoImmutable(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"a.py": "print('immutable test')",
	})

	beforeEntries, _ := os.ReadDir(repo)

	resolver := NewEntryPointResolver()
	_, err := resolver.ResolveEntryPoints(context.Background(), repo, &RepositoryAnalysis{PrimaryLanguage: "python"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	afterEntries, _ := os.ReadDir(repo)

	if len(beforeEntries) != len(afterEntries) {
		t.Errorf("repository contents changed during analysis: before %d, after %d",
			len(beforeEntries), len(afterEntries))
	}
}

func TestBuildPlanner_WithEntryPointResolver_SingleAPy(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"a.py": "print('Hello world')",
	})

	planner := NewBuildPlanner()
	plan, err := planner.PlanBuild(context.Background(), "sb-test", repo, nil)
	if err != nil {
		t.Fatalf("PlanBuild error: %v", err)
	}

	if plan.StartCommand != "python a.py" {
		t.Errorf("expected StartCommand 'python a.py', got %q", plan.StartCommand)
	}
	if plan.ExecutionMode != ExecutionModeScript {
		t.Errorf("expected ExecutionModeScript, got %s", plan.ExecutionMode)
	}
	if plan.PreserveImageEntrypoint {
		t.Errorf("expected PreserveImageEntrypoint=false so start command is used, got true")
	}
}

func TestContextStaging_InjectedRailpackJSON_ForScript(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"a.py": "print('Hello world')",
	})

	planner := NewBuildPlanner()
	plan, err := planner.PlanBuild(context.Background(), "sb-test", repo, nil)
	if err != nil {
		t.Fatalf("PlanBuild error: %v", err)
	}

	stagingEngine := NewContextStagingEngine()
	policy := DefaultContextPolicy()

	stagingRes, err := stagingEngine.Stage(context.Background(), plan, "sb-test", "dep-test", repo, policy)
	if err != nil {
		t.Fatalf("Stage error: %v", err)
	}
	defer stagingRes.Cleanup()

	// Verify staged railpack.json exists in staging dir
	stagedRP := filepath.Join(stagingRes.StagingDir, "railpack.json")
	data, err := os.ReadFile(stagedRP)
	if err != nil {
		t.Fatalf("staged railpack.json should exist: %v", err)
	}

	if !strings.Contains(string(data), "python a.py") {
		t.Errorf("staged railpack.json should contain 'python a.py', got: %s", string(data))
	}

	// Verify original repo NEVER has railpack.json
	origRP := filepath.Join(repo, "railpack.json")
	if _, err := os.Stat(origRP); !os.IsNotExist(err) {
		t.Errorf("original repository must not have railpack.json written to it")
	}
}
