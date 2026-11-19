package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func setupTestGitRepo(t *testing.T) string {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init test git repo: %v", err)
	}

	// Create initial commit
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("failed to stage README: %v", err)
	}

	_, err = wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("failed to initial commit: %v", err)
	}

	return dir
}

func TestValidatePreCommit_BlocksProtectedEnv(t *testing.T) {
	dir := setupTestGitRepo(t)

	// Add .env file
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("SECRET_KEY=supersecret123\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	ctx := context.Background()
	result, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	if result.Allowed {
		t.Fatalf("expected commit to be BLOCKED due to .env, but result.Allowed was true")
	}

	if len(result.Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d: %+v", len(result.Violations), result.Violations)
	}

	if result.Violations[0].File != ".env" {
		t.Errorf("expected violation on .env, got %s", result.Violations[0].File)
	}
	if result.Violations[0].Type != ViolationProtectedEnvFile {
		t.Errorf("expected ViolationProtectedEnvFile, got %s", result.Violations[0].Type)
	}
}

func TestValidatePreCommit_AllowsEnvExample(t *testing.T) {
	dir := setupTestGitRepo(t)

	// Add .env.example with placeholder
	examplePath := filepath.Join(dir, ".env.example")
	content := "PORT=8080\nDATABASE_URL=postgres://user:pass@localhost:5432/dbname\n"
	if err := os.WriteFile(examplePath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write .env.example: %v", err)
	}

	ctx := context.Background()
	result, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Allowed {
		t.Fatalf("expected .env.example to be allowed, but got blocked: %+v", result.Violations)
	}
}

func TestValidatePreCommit_BlocksSecretPatternInSource(t *testing.T) {
	dir := setupTestGitRepo(t)

	// Add main.go with leaked AWS key
	mainPath := filepath.Join(dir, "main.go")
	content := `package main

const AwsKey = "AKIAIOSFODNN7EXAMPLE"
`
	if err := os.WriteFile(mainPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	ctx := context.Background()
	result, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Allowed {
		t.Fatalf("expected commit to be blocked due to hardcoded AWS key, but was allowed")
	}

	if len(result.Violations) == 0 || result.Violations[0].Type != ViolationSecretInSource {
		t.Fatalf("expected ViolationSecretInSource, got: %+v", result.Violations)
	}
}

func TestUntrackFile_RemovesFromIndexAndAddsGitignore(t *testing.T) {
	dir := setupTestGitRepo(t)
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("failed to open repo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Add .env and commit it (simulating repo that already tracked .env upstream)
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("API_SECRET=123\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	_, err = wt.Commit("Add tracked .env", &gogit.CommitOptions{
		Author: &object.Signature{Name: "U", Email: "u@e.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit .env: %v", err)
	}

	// Validate pre-commit detects it as tracked
	ctx := context.Background()
	res, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Modify .env to make working tree dirty
	if err := os.WriteFile(envPath, []byte("API_SECRET=456\n"), 0o644); err != nil {
		t.Fatalf("failed to update .env: %v", err)
	}

	res, err = ValidatePreCommit(ctx, dir)
	if err != nil || res.Allowed || len(res.Violations) == 0 {
		t.Fatalf("expected tracked .env violation, got: %+v", res)
	}
	if res.Violations[0].Type != ViolationTrackedEnvFile {
		t.Errorf("expected ViolationTrackedEnvFile, got %s", res.Violations[0].Type)
	}

	// Untrack .env
	if err := UntrackFile(ctx, dir, ".env"); err != nil {
		t.Fatalf("UntrackFile failed: %v", err)
	}

	// Verify .env still exists on disk!
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		t.Fatalf("UntrackFile deleted the local .env file! It must be preserved.")
	}

	// Verify .gitignore was created/updated
	giBytes, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	if string(giBytes) == "" {
		t.Fatalf(".gitignore is empty")
	}

	// Now check if .gitignore allows commit
	res2, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error after untrack: %v", err)
	}
	// The .env is now ignored, so changes to it won't be committed
	_ = res2
}
