package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// ContextStagingEngine creates isolated, immutable build contexts from a repository.
type ContextStagingEngine struct{}

// NewContextStagingEngine creates a new ContextStagingEngine.
func NewContextStagingEngine() *ContextStagingEngine {
	return &ContextStagingEngine{}
}

// stagingBaseDir is the root under which all staging directories are created.
const stagingBaseDir = "/tmp/api-sandbox-links/staging"

// Stage creates a unique staging directory for the given plan and repository.
// It NEVER modifies repoDir.
// The caller must call result.Cleanup() when done.
func (s *ContextStagingEngine) Stage(
	ctx context.Context,
	plan *ResolvedBuildPlan,
	sandboxID, deployID string,
	repoDir string,
	policy ContextPolicy,
) (*StagingResult, error) {
	// Validate inputs.
	if plan == nil {
		return nil, fmt.Errorf("context staging: plan must not be nil")
	}
	if repoDir == "" {
		return nil, fmt.Errorf("context staging: repoDir must not be empty")
	}
	if _, err := os.Stat(repoDir); err != nil {
		return nil, fmt.Errorf("context staging: repoDir %q: %w", repoDir, err)
	}

	// Build ignore engine.
	engine := NewIgnoreEngine(repoDir, plan.SelectedBuilder, &policy)

	// Inspect repository.
	inspector := NewContextInspector()
	inspection, err := inspector.Inspect(ctx, repoDir, engine, policy)
	if err != nil {
		return nil, fmt.Errorf("context staging: inspection failed: %w", err)
	}

	// Create unique staging directory.
	stagingDir, err := makeStagingDir(sandboxID, deployID)
	if err != nil {
		return nil, fmt.Errorf("context staging: cannot create staging dir: %w", err)
	}

	result := &StagingResult{
		StagingDir:        stagingDir,
		ExcludedEntries:   inspection.ExcludedEntries,
		Warnings:          append([]string{}, inspection.Warnings...),
		Errors:            append([]string{}, inspection.Errors...),
		OriginalFileCount: len(inspection.Files) + len(inspection.ExcludedEntries),
		OriginalSizeBytes: inspection.TotalSize,
		cleanupFn: func() error {
			return os.RemoveAll(stagingDir)
		},
	}

	// Copy each file into staging dir.
	for _, f := range inspection.Files {
		destPath := filepath.Join(stagingDir, filepath.FromSlash(f.RelPath))

		// Create parent directories.
		if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mkdir for %q: %v", f.RelPath, err))
			continue
		}

		// Copy file.
		if err := copyFile(f.AbsPath, destPath, f.Mode); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("copy %q: %v", f.RelPath, err))
			continue
		}

		result.StagedFileCount++
		result.StagedSizeBytes += f.Size
	}

	// Inject Railpack runtime configuration into staging dir only.
	if plan.SelectedBuilder == "railpack" && plan.Runtime.Version != "" {
		pyVersionPath := filepath.Join(stagingDir, ".python-version")
		if err := os.WriteFile(pyVersionPath, []byte(plan.Runtime.Version+"\n"), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("inject .python-version: %v", err))
		} else {
			result.Warnings = append(result.Warnings,
				"injected .python-version into staging context (not written to original repo)")
		}
	}

	// Compute deterministic fingerprint.
	result.Fingerprint = computeFingerprint(inspection.Files, plan.SelectedBuilder, plan.Runtime.Version)

	return result, nil
}

// makeStagingDir creates a unique staging directory path and all parents.
func makeStagingDir(sandboxID, deployID string) (string, error) {
	// Generate a random UUID-like suffix using crypto/rand.
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	suffix := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])

	sid := sandboxID
	if sid == "" {
		sid = "nosandbox"
	}
	did := deployID
	if did == "" {
		did = "nodeploy"
	}

	dirName := fmt.Sprintf("%s-%s-%s", sid, did, suffix)
	stagingDir := filepath.Join(stagingBaseDir, dirName)

	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return "", err
	}
	return stagingDir, nil
}

// copyFile copies src to dst preserving the given file mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// computeFingerprint produces a SHA-256 fingerprint of sorted file paths + sizes + builder + runtime.
// Timestamps are intentionally excluded for determinism.
func computeFingerprint(files []InspectedFile, builder, runtimeVersion string) string {
	// Sort by relative path for determinism.
	sorted := make([]InspectedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})

	h := sha256.New()
	for _, f := range sorted {
		_, _ = fmt.Fprintf(h, "%s:%s\n", f.RelPath, strconv.FormatInt(f.Size, 10))
	}
	_, _ = fmt.Fprintf(h, "builder:%s\n", builder)
	_, _ = fmt.Fprintf(h, "runtime:%s\n", runtimeVersion)

	return hex.EncodeToString(h.Sum(nil))
}
