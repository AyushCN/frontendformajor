package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanner_KrishiSenseFixture(t *testing.T) {
	tmpDir := t.TempDir()

	krishiSenseReq := `# KrishiSense requirements
Flask>=3.0.3
numpy>=2.0.0
pandas>=2.2.0
scikit-learn>=1.5.0
joblib>=1.4.0
xgboost>=2.1.0
tensorflow>=2.17.0
statsmodels>=0.14.0
onnxruntime>=1.19.0
Pillow>=10.3.0
`
	reqPath := filepath.Join(tmpDir, "requirements.txt")
	if err := os.WriteFile(reqPath, []byte(krishiSenseReq), 0o644); err != nil {
		t.Fatalf("failed to write requirements: %v", err)
	}

	planner := NewBuildPlanner()
	userCfg := &UserBuildConfig{
		StartCmd: "python app.py",
		Port:     5000,
		Env: map[string]string{
			"DATABASE_URL": "postgres://user:secret123@127.0.0.1:5432/db",
			"DEBUG":        "false",
		},
	}

	plan, err := planner.PlanBuild(context.Background(), "sb-test-1", tmpDir, userCfg)
	if err != nil {
		t.Fatalf("PlanBuild failed: %v", err)
	}

	// 1. Builder Selection: Automatic Railpack
	if plan.SelectedBuilder != "railpack" || plan.BuilderSelectionSource != "AUTOMATIC_RAILPACK" {
		t.Errorf("expected automatic railpack, got %s (%s)", plan.SelectedBuilder, plan.BuilderSelectionSource)
	}

	// 2. Runtime Resolution: Python 3.11.9 inferred for TensorFlow 2.17 + Scikit-Learn 1.5
	if plan.Runtime.Version != "3.11.9" || plan.Runtime.Source != SourceInferredML {
		t.Errorf("expected inferred Python 3.11.9, got %s (%s)", plan.Runtime.Version, plan.Runtime.Source)
	}

	// 3. Dockerfile Recommendation: Triggered because of deep learning + system libs
	if !plan.Recommendation.Recommended {
		t.Errorf("expected Dockerfile recommendation for heavy deep learning project")
	}

	// 4. Command & Port Preservation
	if plan.StartCommand != "python app.py" || plan.InternalPort != 5000 {
		t.Errorf("expected startCmd 'python app.py' and port 5000, got %q, %d", plan.StartCommand, plan.InternalPort)
	}

	// 5. Sensitive variable redaction
	redacted := plan.RedactedCopy()
	if redacted.EnvironmentVariables["DATABASE_URL"] != "[REDACTED]" {
		t.Errorf("expected DATABASE_URL to be [REDACTED], got %q", redacted.EnvironmentVariables["DATABASE_URL"])
	}
	if redacted.EnvironmentVariables["DEBUG"] != "false" {
		t.Errorf("expected non-sensitive DEBUG to remain 'false', got %q", redacted.EnvironmentVariables["DEBUG"])
	}

	// 6. Immutability: Verify original directory is completely unmodified
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 1 || entries[0].Name() != "requirements.txt" {
		t.Errorf("planner created unexpected files in original repository!")
	}
}

func TestBuildPlanner_StandardFlask(t *testing.T) {
	tmpDir := t.TempDir()
	reqPath := filepath.Join(tmpDir, "requirements.txt")
	_ = os.WriteFile(reqPath, []byte("Flask==3.0.0\ngunicorn==21.0.0\n"), 0o644)

	planner := NewBuildPlanner()
	plan, err := planner.PlanBuild(context.Background(), "sb-flask", tmpDir, nil)
	if err != nil {
		t.Fatalf("PlanBuild failed: %v", err)
	}

	if plan.Category != CategoryStandardPython {
		t.Errorf("expected STANDARD_PYTHON, got %s", plan.Category)
	}
	if plan.Recommendation.Recommended {
		t.Errorf("expected recommendation=false for standard Flask app")
	}
	if plan.Runtime.Version != "3.12.7" {
		t.Errorf("expected default Python 3.12.7 for standard app, got %s", plan.Runtime.Version)
	}
}

func TestBuildPlanner_DeepCopyAndAdversarialRedaction(t *testing.T) {
	plan := &ResolvedBuildPlan{
		ID:        "plan-123",
		SandboxID: "sb-secure",
		StartCommand: "python app.py --token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.t-IDc1wACtAv98ffzs1r7944ce2f0088",
		BuildCommand: "npm run build --key=SUPER_SECRET_KEY_12345",
		EnvironmentVariables: map[string]string{
			"DATABASE_URL":    "postgres://admin:TopSecretPassword123@10.0.0.5:5432/production_db",
			"REDIS_URL":       "redis://default:MyRedisPass@redis.internal:6379",
			"Api_Key":         "abcdef1234567890abcdef1234567890",
			"CUSTOM_AUTH":     "Bearer eyJhbGciOiJIUzI1NiJ9.test",
			"NORMAL_SETTING":  "production",
			"ALLOWED_HOSTS":   "localhost,127.0.0.1",
		},
		Runtime: RuntimeResolution{
			Constraints: []string{"tensorflow >= 2.16"},
			Warnings:    []string{"GPU optional"},
		},
	}

	// 1. Verify Deep Copy Immutability
	cloned := plan.Clone()
	cloned.Runtime.Constraints[0] = "MODIFIED_CONSTRAINT"
	cloned.EnvironmentVariables["NORMAL_SETTING"] = "MODIFIED_SETTING"

	if plan.Runtime.Constraints[0] == "MODIFIED_CONSTRAINT" {
		t.Errorf("plan was mutated through cloned slice reference!")
	}
	if plan.EnvironmentVariables["NORMAL_SETTING"] == "MODIFIED_SETTING" {
		t.Errorf("plan was mutated through cloned map reference!")
	}

	// 2. Verify Adversarial Redaction
	redacted := plan.RedactedCopy()

	// Connection strings with embedded passwords
	if strings.Contains(redacted.EnvironmentVariables["DATABASE_URL"], "TopSecretPassword123") {
		t.Errorf("DATABASE_URL password was not redacted: %s", redacted.EnvironmentVariables["DATABASE_URL"])
	}
	if !strings.Contains(redacted.EnvironmentVariables["DATABASE_URL"], "[REDACTED]") {
		t.Errorf("DATABASE_URL should contain [REDACTED]: %s", redacted.EnvironmentVariables["DATABASE_URL"])
	}
	if strings.Contains(redacted.EnvironmentVariables["REDIS_URL"], "MyRedisPass") {
		t.Errorf("REDIS_URL password was not redacted: %s", redacted.EnvironmentVariables["REDIS_URL"])
	}

	// Mixed-case key
	if redacted.EnvironmentVariables["Api_Key"] != "[REDACTED]" {
		t.Errorf("Api_Key was not redacted: %s", redacted.EnvironmentVariables["Api_Key"])
	}

	// Bearer token
	if strings.Contains(redacted.EnvironmentVariables["CUSTOM_AUTH"], "eyJhbGci") {
		t.Errorf("Bearer token was not redacted: %s", redacted.EnvironmentVariables["CUSTOM_AUTH"])
	}

	// Non-sensitive settings must remain untouched
	if redacted.EnvironmentVariables["NORMAL_SETTING"] != "production" {
		t.Errorf("NORMAL_SETTING was incorrectly modified: %s", redacted.EnvironmentVariables["NORMAL_SETTING"])
	}
	if redacted.EnvironmentVariables["ALLOWED_HOSTS"] != "localhost,127.0.0.1" {
		t.Errorf("ALLOWED_HOSTS was incorrectly modified: %s", redacted.EnvironmentVariables["ALLOWED_HOSTS"])
	}

	// Commands with embedded tokens
	if strings.Contains(redacted.StartCommand, "eyJhbGciOiJIUzI1NiIsInR5cCI") {
		t.Errorf("StartCommand token was not redacted: %s", redacted.StartCommand)
	}

	// Original plan must remain intact after RedactedCopy()
	if strings.Contains(plan.EnvironmentVariables["DATABASE_URL"], "[REDACTED]") {
		t.Errorf("original plan environment was mutated during redaction!")
	}
}

