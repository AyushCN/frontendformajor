package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeIgnoreTestDir creates a temp directory with optional files, cleaned up by t.
func makeIgnoreTestDir(t *testing.T, files map[string]string) string {
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

func TestIgnoreEngine_DefaultExclusions(t *testing.T) {
	policy := DefaultContextPolicy()
	engine := NewIgnoreEngine(t.TempDir(), "railpack", &policy)

	cases := []struct {
		path  string
		isDir bool
		want  bool // true = excluded
	}{
		{".git/config", false, true},
		{".venv/lib/x.py", false, true},
		{"__pycache__/x.pyc", false, true},
		{"module.pyc", false, true},
		{"node_modules/lodash/index.js", false, true},
		{"src/main.py", false, false},
		{"requirements.txt", false, false},
	}

	for _, tc := range cases {
		excluded, _, _ := engine.ShouldExclude(tc.path, tc.isDir)
		if excluded != tc.want {
			t.Errorf("ShouldExclude(%q, %v) = %v, want %v", tc.path, tc.isDir, excluded, tc.want)
		}
	}
}

func TestIgnoreEngine_DockerignoreLoaded(t *testing.T) {
	dir := makeIgnoreTestDir(t, map[string]string{
		".dockerignore": "*.log\n",
	})
	policy := DefaultContextPolicy()
	engine := NewIgnoreEngine(dir, "dockerfile", &policy)

	excluded, _, _ := engine.ShouldExclude("app.log", false)
	if !excluded {
		t.Error("app.log should be excluded by .dockerignore pattern *.log")
	}

	excluded, _, _ = engine.ShouldExclude("app.py", false)
	if excluded {
		t.Error("app.py should not be excluded")
	}
}

func TestIgnoreEngine_RailpackignoreLoaded(t *testing.T) {
	dir := makeIgnoreTestDir(t, map[string]string{
		".railpackignore": "*.tmp\n",
	})
	policy := DefaultContextPolicy()
	engine := NewIgnoreEngine(dir, "railpack", &policy)

	excluded, _, _ := engine.ShouldExclude("build.tmp", false)
	if !excluded {
		t.Error("build.tmp should be excluded by .railpackignore pattern *.tmp")
	}

	excluded, _, _ = engine.ShouldExclude("build.py", false)
	if excluded {
		t.Error("build.py should not be excluded")
	}
}

func TestIgnoreEngine_NegationRules(t *testing.T) {
	dir := makeIgnoreTestDir(t, map[string]string{
		".dockerignore": "*.pyc\n!important.pyc\n",
	})
	policy := DefaultContextPolicy()
	engine := NewIgnoreEngine(dir, "dockerfile", &policy)

	// ordinary.pyc should be excluded
	excluded, _, _ := engine.ShouldExclude("ordinary.pyc", false)
	if !excluded {
		t.Error("ordinary.pyc should be excluded by *.pyc rule")
	}

	// important.pyc should be un-excluded by !important.pyc negation
	excluded, _, _ = engine.ShouldExclude("important.pyc", false)
	if excluded {
		t.Error("important.pyc should NOT be excluded due to negation rule !important.pyc")
	}
}

func TestIgnoreEngine_PreservationWinsOverDefault(t *testing.T) {
	// app.db matches DefaultContextPolicy preserved patterns (*.db)
	// Default exclusions do NOT include *.db, but let's verify preservation
	// works correctly even if we add *.db to excluded patterns.
	policy := DefaultContextPolicy()
	policy.ExcludedPatterns = append(policy.ExcludedPatterns, "*.db")

	engine := NewIgnoreEngine(t.TempDir(), "railpack", &policy)

	excluded, _, _ := engine.ShouldExclude("app.db", false)
	if excluded {
		t.Error("app.db should NOT be excluded: preservation list (*.db) wins over default exclusion")
	}
}

func TestIgnoreEngine_UserIgnoreCanOverridePreservation(t *testing.T) {
	// A user-supplied .dockerignore can override preservation.
	dir := makeIgnoreTestDir(t, map[string]string{
		".dockerignore": "*.db\n",
	})
	policy := DefaultContextPolicy()
	engine := NewIgnoreEngine(dir, "dockerfile", &policy)

	excluded, _, _ := engine.ShouldExclude("app.db", false)
	if !excluded {
		t.Error("app.db SHOULD be excluded when user explicitly adds *.db to .dockerignore")
	}
}

func TestIgnoreEngine_MalformedPattern(t *testing.T) {
	policy := DefaultContextPolicy()
	policy.ExcludedPatterns = append(policy.ExcludedPatterns, "[invalid", "***", "")

	// Should not panic.
	engine := NewIgnoreEngine(t.TempDir(), "railpack", &policy)

	// Verify basic operation still works.
	excluded, _, _ := engine.ShouldExclude(".git/config", false)
	if !excluded {
		t.Error(".git/config should still be excluded despite malformed patterns")
	}
}

func TestIgnoreEngine_LargeIgnoreFile(t *testing.T) {
	// Write a .dockerignore that is > 1 MB.
	dir := t.TempDir()
	var sb strings.Builder
	// 1MB + a bit extra. Each line "# comment\n" is 10 bytes, so ~110k lines for 1MB.
	line := "# padding comment line to fill size\n"
	for sb.Len() < 1024*1024+100 {
		sb.WriteString(line)
	}
	sb.WriteString("*.log\n") // actual rule after the limit — may or may not be loaded

	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(sb.String()), 0644); err != nil {
		t.Fatalf("write large dockerignore: %v", err)
	}

	policy := DefaultContextPolicy()
	// Should not crash regardless of whether *.log is loaded.
	engine := NewIgnoreEngine(dir, "dockerfile", &policy)

	// Basic sanity: default rules still work.
	excluded, _, _ := engine.ShouldExclude(".git/config", false)
	if !excluded {
		t.Error(".git/config should be excluded by default rules")
	}
	_ = engine // silence unused warning
}
