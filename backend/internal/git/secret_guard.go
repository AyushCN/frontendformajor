package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/api-sandbox-links/backend/internal/security"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ViolationType describes the category of commit security block.
type ViolationType string

const (
	ViolationProtectedEnvFile ViolationType = "PROTECTED_ENV_FILE"
	ViolationTrackedEnvFile   ViolationType = "TRACKED_ENV_FILE"
	ViolationSecretInSource   ViolationType = "SECRET_IN_SOURCE"
)

// CommitViolation represents one blocked security issue.
type CommitViolation struct {
	File        string        `json:"file"`
	Type        ViolationType `json:"type"`
	Message     string        `json:"message"`
	IsTracked   bool          `json:"is_tracked"`
	Line        int           `json:"line,omitempty"`
	Remediation string        `json:"remediation"`
}

// ValidationResult encapsulates the verdict of pre-commit scanning.
type ValidationResult struct {
	Allowed    bool              `json:"allowed"`
	Violations []CommitViolation `json:"violations,omitempty"`
}

// ValidatePreCommit scans worktree status and proposed changes for protected files and leaked credentials.
// It executes purely in-memory before committing, ensuring no secrets reach the Git commit object.
func ValidatePreCommit(ctx context.Context, dir string) (*ValidationResult, error) {
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("git: opening repo at %s: %w", dir, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("git: opening worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("git: reading status: %w", err)
	}

	headRef, headErr := repo.Head()
	var headCommit *object.Commit
	if headErr == nil {
		headCommit, _ = repo.CommitObject(headRef.Hash())
	}

	var violations []CommitViolation

	for path, fileStatus := range status {
		// Ignore deleted files
		if fileStatus.Worktree == git.Deleted || fileStatus.Staging == git.Deleted {
			continue
		}

		cleanPath := filepath.ToSlash(path)

		// 1. Check if file is a protected environment file
		if security.IsProtectedEnvFile(cleanPath) {
			isTracked := false
			if headCommit != nil {
				if _, err := headCommit.File(cleanPath); err == nil {
					isTracked = true
				}
			}

			if isTracked {
				violations = append(violations, CommitViolation{
					File:      cleanPath,
					Type:      ViolationTrackedEnvFile,
					Message:   fmt.Sprintf("Protected environment file %q is already tracked in Git history.", cleanPath),
					IsTracked: true,
					Remediation: fmt.Sprintf(
						"Remove %q from Git tracking while preserving your local copy, and ensure it is listed in .gitignore.",
						cleanPath,
					),
				})
			} else {
				violations = append(violations, CommitViolation{
					File:      cleanPath,
					Type:      ViolationProtectedEnvFile,
					Message:   fmt.Sprintf("Protected environment file %q cannot be committed.", cleanPath),
					IsTracked: false,
					Remediation: fmt.Sprintf(
						"Add %q to .gitignore so it remains local and is never committed to source control.",
						cleanPath,
					),
				})
			}
			continue
		}

		// 2. Scan content of modified or newly added files for raw credential patterns
		fullPath := filepath.Join(dir, filepath.FromSlash(cleanPath))
		info, sErr := os.Stat(fullPath)
		if sErr != nil || info.IsDir() {
			continue
		}

		// Limit content scan size to 1 MB per file
		if info.Size() > 1024*1024 {
			continue
		}

		contentBytes, rErr := os.ReadFile(fullPath)
		if rErr != nil {
			continue
		}

		findings := security.ScanContentForSecrets(string(contentBytes))
		for _, f := range findings {
			violations = append(violations, CommitViolation{
				File:        cleanPath,
				Type:        ViolationSecretInSource,
				Line:        f.Line,
				Message:     fmt.Sprintf("Potential %s credential detected in %s at line %d.", f.Type, cleanPath, f.Line),
				Remediation: "Remove hardcoded credentials from source code and use environment variables instead.",
			})
		}
	}

	return &ValidationResult{
		Allowed:    len(violations) == 0,
		Violations: violations,
	}, nil
}

// UntrackFile removes filePath from the Git index while leaving the file on disk intact.
// It also appends filePath to .gitignore if not already ignored.
func UntrackFile(ctx context.Context, dir, relPath string) error {
	cleanPath := filepath.ToSlash(filepath.Clean(relPath))

	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return fmt.Errorf("git: opening repo at %s: %w", dir, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git: opening worktree: %w", err)
	}

	// 1. Check if tracked in index
	idx, err := repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("git: reading index: %w", err)
	}

	entryFound := false
	for _, entry := range idx.Entries {
		if entry.Name == cleanPath {
			entryFound = true
			break
		}
	}

	// Backup file content before removing from index
	fullPath := filepath.Join(dir, filepath.FromSlash(cleanPath))
	backup, readErr := os.ReadFile(fullPath)

	if entryFound {
		// Remove from index
		if _, err := wt.Remove(cleanPath); err != nil {
			return fmt.Errorf("git: removing %s from index: %w", cleanPath, err)
		}
		// Restore the local file on disk so the developer's workspace is preserved intact
		if readErr == nil {
			_ = os.WriteFile(fullPath, backup, 0o644)
		}
	}

	// 2. Ensure .gitignore contains the entry
	gitignorePath := filepath.Join(dir, ".gitignore")
	var existingGitignore string
	if b, err := os.ReadFile(gitignorePath); err == nil {
		existingGitignore = string(b)
	}

	lines := strings.Split(existingGitignore, "\n")
	alreadyIgnored := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == cleanPath || trimmed == "/"+cleanPath || (cleanPath == ".env" && (trimmed == ".env" || trimmed == ".env*")) {
			alreadyIgnored = true
			break
		}
	}

	if !alreadyIgnored {
		var toAppend strings.Builder
		if len(existingGitignore) > 0 && !strings.HasSuffix(existingGitignore, "\n") {
			toAppend.WriteString("\n")
		}
		toAppend.WriteString("# Local environment files\n")
		toAppend.WriteString(cleanPath + "\n")

		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("git: updating .gitignore: %w", err)
		}
		defer f.Close()
		if _, err := io.WriteString(f, toAppend.String()); err != nil {
			return fmt.Errorf("git: writing to .gitignore: %w", err)
		}
	}

	return nil
}

// SafeCommitAll runs PreCommit validation before staging and committing.
// If violations are found, it aborts without modifying staged state or repository history.
func SafeCommitAll(ctx context.Context, dir, message string) (string, *ValidationResult, error) {
	validation, err := ValidatePreCommit(ctx, dir)
	if err != nil {
		return "", nil, err
	}

	if !validation.Allowed {
		return "", validation, nil
	}

	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", nil, fmt.Errorf("git: opening repo at %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", nil, fmt.Errorf("git: opening worktree: %w", err)
	}

	// Filtered staging: stage all files except any protected environment files that might be unignored
	status, err := wt.Status()
	if err != nil {
		return "", nil, fmt.Errorf("git: reading status: %w", err)
	}

	if status.IsClean() {
		return "", validation, ErrNoChanges
	}

	for path := range status {
		clean := filepath.ToSlash(path)
		if security.IsProtectedEnvFile(clean) {
			continue // Never stage protected environment files
		}
		if err := wt.AddWithOptions(&git.AddOptions{Path: clean}); err != nil {
			// Skip files that might have been removed
			continue
		}
	}

	newStatus, err := wt.Status()
	if err != nil {
		return "", nil, fmt.Errorf("git: reading status after staging: %w", err)
	}

	// Verify something is staged
	hasStaged := false
	for _, fs := range newStatus {
		if fs.Staging != git.Unmodified && fs.Staging != git.Untracked {
			hasStaged = true
			break
		}
	}
	if !hasStaged {
		return "", validation, ErrNoChanges
	}

	hash, err := wt.Commit(message, &git.CommitOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("git: committing: %w", err)
	}

	return hash.String(), validation, nil
}
