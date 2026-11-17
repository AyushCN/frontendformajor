package orchestrator

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// InspectedFile records metadata about a single file accepted for staging.
type InspectedFile struct {
	RelPath    string
	AbsPath    string
	Size       int64
	Mode       os.FileMode
	IsSymlink  bool
	LinkTarget string // resolved absolute path, only if IsSymlink
}

// ContextInspectionResult is the immutable output of a repository walk.
type ContextInspectionResult struct {
	Files           []InspectedFile
	TotalSize       int64
	ExcludedEntries []ExcludedEntry
	Warnings        []string
	Errors          []string
}

// ContextInspector walks a repository directory applying policy and ignore rules.
type ContextInspector struct{}

// NewContextInspector creates a new ContextInspector.
func NewContextInspector() *ContextInspector {
	return &ContextInspector{}
}

// inodeKey uniquely identifies a filesystem node to detect symlink loops.
type inodeKey struct {
	dev uint64
	ino uint64
}

// Inspect walks repoDir, applying policy and engine, and returns an immutable result.
// Never modifies repoDir. Returns error only for fatal conditions (e.g. repoDir unreadable).
// Per-file problems are recorded in result.Errors, not returned as errors.
func (ci *ContextInspector) Inspect(
	ctx context.Context,
	repoDir string,
	engine *IgnoreEngine,
	policy ContextPolicy,
) (*ContextInspectionResult, error) {
	// Canonicalize root to resolve any symlinks in the base path itself.
	canonicalRoot, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, fmt.Errorf("context inspector: cannot canonicalize repo dir %q: %w", repoDir, err)
	}

	result := &ContextInspectionResult{
		Files:           make([]InspectedFile, 0),
		ExcludedEntries: make([]ExcludedEntry, 0),
		Warnings:        make([]string, 0),
		Errors:          make([]string, 0),
	}

	visitedInodes := make(map[inodeKey]struct{})

	err = filepath.WalkDir(canonicalRoot, func(absPath string, d fs.DirEntry, walkErr error) error {
		// Check context cancellation on every entry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if walkErr != nil {
			if absPath == canonicalRoot {
				return fmt.Errorf("context inspector: cannot read root dir: %w", walkErr)
			}
			relPath, _ := filepath.Rel(canonicalRoot, absPath)
			result.Errors = append(result.Errors, fmt.Sprintf("walk error at %q: %v", relPath, walkErr))
			return nil
		}

		// Skip the root itself.
		if absPath == canonicalRoot {
			return nil
		}

		relPath, err := filepath.Rel(canonicalRoot, absPath)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Compute depth (number of path segments).
		depth := len(strings.Split(relPath, "/"))
		if depth > policy.MaxDepth {
			result.ExcludedEntries = append(result.ExcludedEntries, ExcludedEntry{
				RelativePath: relPath,
				Reason:       "DEPTH_LIMIT",
			})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		isDir := d.IsDir()

		// Apply ignore engine.
		excluded, reason, pattern := engine.ShouldExclude(relPath, isDir)
		if excluded {
			result.ExcludedEntries = append(result.ExcludedEntries, ExcludedEntry{
				RelativePath: relPath,
				Reason:       reason,
				Pattern:      pattern,
			})
			if isDir {
				return fs.SkipDir
			}
			return nil
		}

		// Get file info (Lstat to detect symlinks before resolving).
		info, err := os.Lstat(absPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("lstat %q: %v", relPath, err))
			return nil
		}

		mode := info.Mode()

		// Detect special files: named pipes, sockets, devices.
		if mode&(os.ModeNamedPipe|os.ModeSocket|os.ModeDevice|os.ModeCharDevice) != 0 {
			result.ExcludedEntries = append(result.ExcludedEntries, ExcludedEntry{
				RelativePath: relPath,
				Reason:       "SPECIAL_FILE",
			})
			return nil
		}

		// Detect symlinks.
		if mode&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(absPath)
			if err != nil {
				result.ExcludedEntries = append(result.ExcludedEntries, ExcludedEntry{
					RelativePath: relPath,
					Reason:       "SYMLINK_BROKEN",
				})
				return nil
			}

			// Reject symlinks that escape the repo root.
			cleanResolved := filepath.Clean(resolved)
			if cleanResolved != canonicalRoot && !strings.HasPrefix(cleanResolved+string(os.PathSeparator), canonicalRoot+string(os.PathSeparator)) {
				result.ExcludedEntries = append(result.ExcludedEntries, ExcludedEntry{
					RelativePath: relPath,
					Reason:       "UNSAFE_SYMLINK",
				})
				return nil
			}

			// Detect symlink loops via inode tracking.
			var st syscall.Stat_t
			if err := syscall.Stat(resolved, &st); err == nil {
				key := inodeKey{dev: uint64(st.Dev), ino: st.Ino}
				if _, seen := visitedInodes[key]; seen {
					result.Warnings = append(result.Warnings, fmt.Sprintf("symlink loop detected at %q, skipping", relPath))
					return nil
				}
				visitedInodes[key] = struct{}{}
			}

			// Get info of the resolved target.
			targetInfo, err := os.Stat(resolved)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("stat symlink target %q: %v", relPath, err))
				return nil
			}

			// If target is a directory, skip — directories are created implicitly.
			if targetInfo.IsDir() {
				return nil
			}

			// Stage as a regular file (copy the target's content).
			fileSize := targetInfo.Size()
			if fileSize > policy.MaxSingleFileBytes {
				result.Warnings = append(result.Warnings, fmt.Sprintf("file %q size %d exceeds single-file limit %d (advisory only)", relPath, fileSize, policy.MaxSingleFileBytes))
			}

			result.TotalSize += fileSize
			if result.TotalSize > policy.MaxContextBytes {
				return fmt.Errorf("repository exceeds maximum context size (%d bytes)", policy.MaxContextBytes)
			}

			if len(result.Files) >= policy.MaxFileCount {
				return fmt.Errorf("repository exceeds maximum file count (%d)", policy.MaxFileCount)
			}

			result.Files = append(result.Files, InspectedFile{
				RelPath:    relPath,
				AbsPath:    resolved,
				Size:       fileSize,
				Mode:       targetInfo.Mode(),
				IsSymlink:  true,
				LinkTarget: resolved,
			})
			return nil
		}

		// Skip directories (created implicitly during staging).
		if isDir {
			// Track inode to detect hard-link loops in directories (rare but possible).
			var st syscall.Stat_t
			if err := syscall.Stat(absPath, &st); err == nil {
				key := inodeKey{dev: uint64(st.Dev), ino: st.Ino}
				if _, seen := visitedInodes[key]; seen {
					result.Warnings = append(result.Warnings, fmt.Sprintf("directory inode loop at %q, skipping subtree", relPath))
					return fs.SkipDir
				}
				visitedInodes[key] = struct{}{}
			}
			return nil
		}

		// Regular file.
		fileSize := info.Size()
		if fileSize > policy.MaxSingleFileBytes {
			result.Warnings = append(result.Warnings, fmt.Sprintf("file %q size %d exceeds single-file limit %d (advisory only)", relPath, fileSize, policy.MaxSingleFileBytes))
		}

		result.TotalSize += fileSize
		if result.TotalSize > policy.MaxContextBytes {
			return fmt.Errorf("repository exceeds maximum context size (%d bytes)", policy.MaxContextBytes)
		}

		if len(result.Files) >= policy.MaxFileCount {
			return fmt.Errorf("repository exceeds maximum file count (%d)", policy.MaxFileCount)
		}

		result.Files = append(result.Files, InspectedFile{
			RelPath: relPath,
			AbsPath: absPath,
			Size:    fileSize,
			Mode:    mode,
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
