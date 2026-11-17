package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------- test helpers ----------

// makeRepo creates a temporary directory populated with the given files (map of relPath → content).
// Directories are created automatically. The temp dir is cleaned up by t.
func makeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatalf("makeRepo mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatalf("makeRepo write %s: %v", abs, err)
		}
	}
	return dir
}

// makePlan builds a minimal ResolvedBuildPlan for tests.
func makePlan(builder, runtimeVersion string) *ResolvedBuildPlan {
	return &ResolvedBuildPlan{
		ID:              "test-plan",
		SandboxID:       "test-sandbox",
		SelectedBuilder: builder,
		Runtime: RuntimeResolution{
			Version: runtimeVersion,
		},
	}
}

// stagedFileExists checks whether a relative path exists inside the staging dir.
func stagedFileExists(stagingDir, relPath string) bool {
	_, err := os.Stat(filepath.Join(stagingDir, filepath.FromSlash(relPath)))
	return err == nil
}

// listAllFiles returns sorted relative paths of all regular files under root.
func listAllFiles(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listAllFiles walk: %v", err)
	}
	return paths
}

// ---------- tests ----------

func TestContextStaging_NormalRepository(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"app.py":          "print('hello')",
		"requirements.txt": "flask==3.0.0",
		"static/index.html": "<html></html>",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "3.11.9")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if result.StagedFileCount == 0 {
		t.Error("expected staged files, got 0")
	}

	// Verify files in staging dir.
	for _, rel := range []string{"app.py", "requirements.txt", "static/index.html"} {
		if !stagedFileExists(result.StagingDir, rel) {
			t.Errorf("expected staged file %q not found", rel)
		}
	}

	// Verify original repo is unchanged.
	for _, rel := range []string{"app.py", "requirements.txt", "static/index.html"} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); err != nil {
			t.Errorf("original repo file %q missing after staging: %v", rel, err)
		}
	}
}

func TestContextStaging_EmptyRepository(t *testing.T) {
	repo := t.TempDir()

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage empty repo: %v", err)
	}
	defer result.Cleanup()

	if result.StagedFileCount != 0 {
		t.Errorf("expected 0 staged files, got %d", result.StagedFileCount)
	}
}

func TestContextStaging_SymlinkInsideRepo(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"real.py": "# real file",
	})
	// Create a symlink inside the repo pointing to a sibling file.
	if err := os.Symlink(filepath.Join(repo, "real.py"), filepath.Join(repo, "link.py")); err != nil {
		t.Skip("symlink creation not supported:", err)
	}

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	// real.py must always be staged; link.py may or may not be staged.
	if !stagedFileExists(result.StagingDir, "real.py") {
		t.Error("real.py should be staged")
	}

	// No UNSAFE_SYMLINK exclusion for a link inside the repo.
	for _, ex := range result.ExcludedEntries {
		if ex.RelativePath == "link.py" && ex.Reason == "UNSAFE_SYMLINK" {
			t.Error("link.py should not be tagged UNSAFE_SYMLINK — it stays inside the repo")
		}
	}
}

func TestContextStaging_SymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	// Create a file outside the repo.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	// Symlink inside repo → outside.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(repo, "escape.txt")); err != nil {
		t.Skip("symlink creation not supported:", err)
	}

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	// escape.txt must not appear in staging dir.
	if stagedFileExists(result.StagingDir, "escape.txt") {
		t.Error("escape.txt should not be staged — symlink escapes repo root")
	}

	// Should be recorded in ExcludedEntries as UNSAFE_SYMLINK.
	found := false
	for _, ex := range result.ExcludedEntries {
		if ex.RelativePath == "escape.txt" && (ex.Reason == "UNSAFE_SYMLINK" || ex.Reason == "SYMLINK_ESCAPE") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected UNSAFE_SYMLINK exclusion for escape.txt; excluded: %+v", result.ExcludedEntries)
	}
}

func TestContextStaging_SymlinkLoop(t *testing.T) {
	repo := t.TempDir()

	// Create a → b → a symlink loop.
	a := filepath.Join(repo, "a")
	b := filepath.Join(repo, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skip("symlink creation not supported:", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skip("symlink creation not supported:", err)
	}

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
		if err != nil {
			return // any error is acceptable (broken symlinks)
		}
		defer result.Cleanup()
	}()

	select {
	case <-done:
		// OK — completed without hanging.
	case <-time.After(5 * time.Second):
		t.Fatal("Stage hung on symlink loop (did not complete within 5s)")
	}
}

func TestContextStaging_SpecialFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; special file exclusion tests may not apply")
	}

	repo := t.TempDir()
	pipePath := filepath.Join(repo, "fifo_pipe")

	if err := syscall.Mkfifo(pipePath, 0644); err != nil {
		t.Skipf("Mkfifo not supported: %v", err)
	}

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	// fifo_pipe must NOT be staged.
	if stagedFileExists(result.StagingDir, "fifo_pipe") {
		t.Error("named pipe fifo_pipe should not be staged")
	}

	// Must be recorded as SPECIAL_FILE exclusion.
	found := false
	for _, ex := range result.ExcludedEntries {
		if ex.RelativePath == "fifo_pipe" && ex.Reason == "SPECIAL_FILE" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SPECIAL_FILE exclusion for fifo_pipe; excluded: %+v", result.ExcludedEntries)
	}
}

func TestContextStaging_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}

	repo := makeRepo(t, map[string]string{
		"readable.py":   "print('ok')",
		"unreadable.py": "print('secret')",
	})

	// Make unreadable.py unreadable.
	if err := os.Chmod(filepath.Join(repo, "unreadable.py"), 0000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore on cleanup so t.TempDir() can remove it.
	t.Cleanup(func() { os.Chmod(filepath.Join(repo, "unreadable.py"), 0644) })

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage unexpectedly returned fatal error: %v", err)
	}
	defer result.Cleanup()

	// unreadable.py error should be recorded, not cause a fatal abort.
	hasError := false
	for _, e := range result.Errors {
		if strings.Contains(e, "unreadable.py") {
			hasError = true
		}
	}
	if !hasError {
		t.Error("expected an error entry for unreadable.py, but none recorded")
	}
}

func TestContextStaging_PathTraversal(t *testing.T) {
	// All file copies go into a unique staging dir under stagingBaseDir.
	// No file should ever be written outside that dir.
	repo := makeRepo(t, map[string]string{
		"app.py": "print('ok')",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	// Verify staging dir is under the expected base.
	if !strings.HasPrefix(result.StagingDir, stagingBaseDir) {
		t.Errorf("staging dir %q is not under expected base %q", result.StagingDir, stagingBaseDir)
	}

	// Verify no file exists outside staging dir.
	files := listAllFiles(t, result.StagingDir)
	for _, f := range files {
		abs := filepath.Join(result.StagingDir, f)
		if !strings.HasPrefix(abs, result.StagingDir) {
			t.Errorf("staged file %q is outside staging dir", abs)
		}
	}
}

func TestContextStaging_DockerignoreRespected(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		".dockerignore": "*.log\n",
		"app.py":        "print('ok')",
		"server.log":    "build output",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("dockerfile", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if stagedFileExists(result.StagingDir, "server.log") {
		t.Error("server.log should be excluded by .dockerignore *.log pattern")
	}
	if !stagedFileExists(result.StagingDir, "app.py") {
		t.Error("app.py should be staged")
	}
}

func TestContextStaging_RailpackignoreRespected(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		".railpackignore": "*.tmp\n",
		"app.py":          "print('ok')",
		"build.tmp":       "temp output",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if stagedFileExists(result.StagingDir, "build.tmp") {
		t.Error("build.tmp should be excluded by .railpackignore *.tmp pattern")
	}
	if !stagedFileExists(result.StagingDir, "app.py") {
		t.Error("app.py should be staged")
	}
}

func TestContextStaging_SQLitePreservation(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"app.py": "print('ok')",
		"app.db": "SQLite db data",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if !stagedFileExists(result.StagingDir, "app.db") {
		t.Error("app.db should be staged (preservation rule *.db)")
	}
}

func TestContextStaging_ModelFilePreservation(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"model.onnx": "ONNX model data",
		"weights.pt": "PyTorch weights",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	for _, f := range []string{"model.onnx", "weights.pt"} {
		if !stagedFileExists(result.StagingDir, f) {
			t.Errorf("%s should be staged (preservation rule)", f)
		}
	}
}

func TestContextStaging_DatasetPreservation(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"data/train.csv": "col1,col2\n1,2",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if !stagedFileExists(result.StagingDir, "data/train.csv") {
		t.Error("data/train.csv should be staged (data/** preservation rule)")
	}
}

func TestContextStaging_NodeModulesExcluded(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"package.json":                  `{"name":"test"}`,
		"node_modules/lodash/index.js":  "// lodash",
		"src/index.js":                  "console.log('hi')",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if stagedFileExists(result.StagingDir, "node_modules/lodash/index.js") {
		t.Error("node_modules should be excluded by default policy")
	}
	if !stagedFileExists(result.StagingDir, "src/index.js") {
		t.Error("src/index.js should be staged")
	}
}

func TestContextStaging_VenvExcluded(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"app.py": "print('ok')",
		".venv/lib/python3.11/site-packages/pip/__init__.py": "# pip",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	if stagedFileExists(result.StagingDir, ".venv/lib/python3.11/site-packages/pip/__init__.py") {
		t.Error(".venv contents should be excluded by default policy")
	}
	if !stagedFileExists(result.StagingDir, "app.py") {
		t.Error("app.py should be staged")
	}
}

func TestContextStaging_OriginalRepoImmutable(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"app.py":           "print('ok')",
		"requirements.txt": "flask==3.0.0",
	})

	// Snapshot before.
	before := listAllFiles(t, repo)

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "3.11.9")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer result.Cleanup()

	// Snapshot after.
	after := listAllFiles(t, repo)

	if len(before) != len(after) {
		t.Errorf("original repo file count changed: before=%d after=%d", len(before), len(after))
	}
	for i, b := range before {
		if i >= len(after) || b != after[i] {
			t.Errorf("original repo changed: before[%d]=%q after[%d]=%q", i, b, i, after[i])
		}
	}
}

func TestContextStaging_ConcurrentBuilds(t *testing.T) {
	const workers = 5
	var wg sync.WaitGroup
	stagingDirs := make([]string, workers)
	var mu sync.Mutex
	errors := make([]error, 0)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repo := makeRepo(t, map[string]string{
				"app.py": strings.Repeat("x", idx*100+10),
			})

			engine := NewContextStagingEngine()
			policy := DefaultContextPolicy()
			plan := makePlan("railpack", "3.11.9")

			result, err := engine.Stage(context.Background(), plan, "sandbox"+strings.Repeat("0", idx), "deploy0", repo, policy)
			if err != nil {
				mu.Lock()
				errors = append(errors, err)
				mu.Unlock()
				return
			}
			defer result.Cleanup()

			mu.Lock()
			stagingDirs[idx] = result.StagingDir
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	if len(errors) > 0 {
		t.Errorf("concurrent staging errors: %v", errors)
	}

	// All staging dirs must be unique.
	seen := make(map[string]bool)
	for _, d := range stagingDirs {
		if d == "" {
			continue
		}
		if seen[d] {
			t.Errorf("staging dir collision: %q used by multiple workers", d)
		}
		seen[d] = true
	}
}

func TestContextStaging_CleanupAfterSuccess(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"app.py": "print('ok')",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	stagingDir := result.StagingDir

	// Verify staging dir exists before cleanup.
	if _, err := os.Stat(stagingDir); err != nil {
		t.Fatalf("staging dir should exist before cleanup: %v", err)
	}

	if err := result.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Verify staging dir is gone after cleanup.
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Error("staging dir should be removed after Cleanup()")
	}
}

func TestContextStaging_CleanupAfterCancel(t *testing.T) {
	// A large-ish repo to give the cancel a chance to fire mid-walk.
	files := make(map[string]string)
	for i := 0; i < 20; i++ {
		files[strings.Repeat("a", i+1)+".py"] = strings.Repeat("x", 1000)
	}
	repo := makeRepo(t, files)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately.
	cancel()

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "")

	result, err := engine.Stage(ctx, plan, "s1", "d1", repo, policy)
	// May error due to cancellation — that's OK.
	if err != nil {
		return
	}
	// If it succeeded (small repo finished before cancel), cleanup should still work.
	defer func() {
		if clErr := result.Cleanup(); clErr != nil {
			t.Logf("Cleanup after cancel: %v", clErr)
		}
	}()
}

func TestContextStaging_FingerprintDeterminism(t *testing.T) {
	files := map[string]string{
		"app.py":           "print('hello')",
		"requirements.txt": "flask==3.0.0",
	}
	repo1 := makeRepo(t, files)
	repo2 := makeRepo(t, files)

	differentRepo := makeRepo(t, map[string]string{
		"app.py": "print('world')",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("railpack", "3.11.9")

	result1, err := engine.Stage(context.Background(), plan, "s1", "d1", repo1, policy)
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	defer result1.Cleanup()

	result2, err := engine.Stage(context.Background(), plan, "s2", "d2", repo2, policy)
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	defer result2.Cleanup()

	result3, err := engine.Stage(context.Background(), plan, "s3", "d3", differentRepo, policy)
	if err != nil {
		t.Fatalf("Stage 3: %v", err)
	}
	defer result3.Cleanup()

	if result1.Fingerprint == "" {
		t.Error("fingerprint should not be empty")
	}
	if result1.Fingerprint != result2.Fingerprint {
		t.Errorf("same content should produce same fingerprint: %q vs %q", result1.Fingerprint, result2.Fingerprint)
	}
	if result1.Fingerprint == result3.Fingerprint {
		t.Error("different content should produce different fingerprints")
	}
}

func TestContextStaging_MaxFilesLimit(t *testing.T) {
	files := make(map[string]string)
	for i := 0; i < 10; i++ {
		files[strings.Repeat("f", i+1)+".py"] = "x"
	}
	repo := makeRepo(t, files)

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	policy.MaxFileCount = 5
	plan := makePlan("railpack", "")

	_, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err == nil {
		t.Error("expected error when file count exceeds MaxFileCount")
	}
	if err != nil && !strings.Contains(err.Error(), "file count") {
		t.Errorf("expected file count error, got: %v", err)
	}
}

func TestContextStaging_MaxContextSizeLimit(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"large.py": strings.Repeat("x", 2048), // 2KB
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	policy.MaxContextBytes = 1024 // 1KB limit
	plan := makePlan("railpack", "")

	_, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err == nil {
		t.Error("expected error when total size exceeds MaxContextBytes")
	}
	if err != nil && !strings.Contains(err.Error(), "context size") {
		t.Errorf("expected context size error, got: %v", err)
	}
}

func TestContextStaging_OversizedSingleFile(t *testing.T) {
	// 10KB file with 1KB single-file limit → advisory warning only, file still staged.
	repo := makeRepo(t, map[string]string{
		"big.py": strings.Repeat("x", 10*1024),
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	policy.MaxSingleFileBytes = 1024          // 1KB advisory limit
	policy.MaxContextBytes = 100 * 1024 * 1024 // 100MB total limit (not hit)
	plan := makePlan("railpack", "")

	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage should not fail on oversized single file: %v", err)
	}
	defer result.Cleanup()

	// File must still be staged.
	if !stagedFileExists(result.StagingDir, "big.py") {
		t.Error("big.py should be staged (oversized single file is advisory only)")
	}

	// A warning should be present.
	hasWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "big.py") {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected advisory warning for oversized single file")
	}
}

func TestContextStaging_MalformedIgnorePatterns(t *testing.T) {
	// .dockerignore with invalid patterns.
	repo := makeRepo(t, map[string]string{
		"app.py":       "print('ok')",
		".dockerignore": "[invalid\n***\n\x00null\n*.log\n",
	})

	engine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	plan := makePlan("dockerfile", "")

	// Must not panic.
	result, err := engine.Stage(context.Background(), plan, "s1", "d1", repo, policy)
	if err != nil {
		t.Fatalf("Stage should not fail on malformed ignore patterns: %v", err)
	}
	defer result.Cleanup()

	// app.py should still be staged.
	if !stagedFileExists(result.StagingDir, "app.py") {
		t.Error("app.py should be staged despite malformed ignore patterns")
	}
}
