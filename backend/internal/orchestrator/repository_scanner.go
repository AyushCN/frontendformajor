package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxScannerFiles = 500
	maxScannerDepth = 3
	maxSnippetBytes = 64 * 1024 // 64 KB bounded read per file
)

// ScannedFile records metadata for a single regular source or config file in the repository.
type ScannedFile struct {
	RelPath   string
	AbsPath   string
	Extension string
	Size      int64
	Depth     int
}

// ScannedRepository holds the structured discovery result from scanning a repository.
type ScannedRepository struct {
	RootDir      string
	Files        []ScannedFile
	FilesByExt   map[string][]ScannedFile
	Manifests    []string // e.g. "requirements.txt", "package.json", "go.mod", "Cargo.toml"
	BuildConfigs []string // e.g. "Makefile", "CMakeLists.txt", "railpack.json", "Procfile"
	Warnings     []string
}

// RepositoryScanner performs bounded, safe filesystem inspection of a repository.
type RepositoryScanner struct {
	ignoredDirs map[string]bool
}

// NewRepositoryScanner constructs a new RepositoryScanner.
func NewRepositoryScanner() *RepositoryScanner {
	return &RepositoryScanner{
		ignoredDirs: map[string]bool{
			".git":          true,
			".venv":         true,
			"venv":          true,
			"node_modules":  true,
			"__pycache__":   true,
			".pytest_cache": true,
			".mypy_cache":   true,
			"target":        true,
			"build":         true,
			"dist":          true,
			".cache":        true,
			".idea":         true,
			".vscode":       true,
		},
	}
}

// Scan performs read-only traversal of repoDir, enforcing symlink isolation and limits.
func (s *RepositoryScanner) Scan(ctx context.Context, repoDir string) (*ScannedRepository, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("repository scanner: repoDir cannot be empty")
	}

	cleanRoot, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("repository scanner: resolving path: %w", err)
	}

	canonicalRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		canonicalRoot = cleanRoot
	}

	res := &ScannedRepository{
		RootDir:      cleanRoot,
		Files:        make([]ScannedFile, 0),
		FilesByExt:   make(map[string][]ScannedFile),
		Manifests:    make([]string, 0),
		BuildConfigs: make([]string, 0),
		Warnings:     make([]string, 0),
	}

	scannedCount := 0

	var walkFn func(currentDir string, depth int) error
	walkFn = func(currentDir string, depth int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if depth > maxScannerDepth || scannedCount >= maxScannerFiles {
			return nil
		}

		entries, err := os.ReadDir(currentDir)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("cannot read dir %s: %v", currentDir, err))
			return nil
		}

		for _, entry := range entries {
			scannedCount++
			if scannedCount >= maxScannerFiles {
				res.Warnings = append(res.Warnings, fmt.Sprintf("reached maximum scan limit (%d files)", maxScannerFiles))
				return nil
			}

			name := entry.Name()
			fullPath := filepath.Join(currentDir, name)

			// Symlink boundary safety check
			if !s.isSafePath(cleanRoot, canonicalRoot, fullPath) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipping unsafe symlink or escape: %s", name))
				continue
			}

			if entry.IsDir() {
				if s.ignoredDirs[name] || strings.HasPrefix(name, ".") {
					continue
				}
				if err := walkFn(fullPath, depth+1); err != nil {
					return err
				}
				continue
			}

			// Regular file
			info, err := entry.Info()
			if err != nil {
				continue
			}

			relPath, err := filepath.Rel(cleanRoot, fullPath)
			if err != nil {
				continue
			}
			relPath = filepath.ToSlash(relPath)

			ext := strings.ToLower(filepath.Ext(name))

			sf := ScannedFile{
				RelPath:   relPath,
				AbsPath:   fullPath,
				Extension: ext,
				Size:      info.Size(),
				Depth:     depth,
			}

			res.Files = append(res.Files, sf)
			if ext != "" {
				res.FilesByExt[ext] = append(res.FilesByExt[ext], sf)
			}

			// Detect manifests and build configs
			lowerName := strings.ToLower(name)
			switch lowerName {
			case "requirements.txt", "pyproject.toml", "pipfile", "package.json", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts", "cargo.toml":
				res.Manifests = append(res.Manifests, relPath)
			case "makefile", "cmakelists.txt", "railpack.json", "procfile", "dockerfile":
				res.BuildConfigs = append(res.BuildConfigs, relPath)
			}
		}

		return nil
	}

	if err := walkFn(cleanRoot, 0); err != nil {
		return nil, err
	}

	return res, nil
}

// ReadSnippet reads up to maxSnippetBytes from a scanned file safely without executing code.
func (s *RepositoryScanner) ReadSnippet(sf ScannedFile) ([]byte, error) {
	f, err := os.Open(sf.AbsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := io.LimitReader(f, maxSnippetBytes)
	buf := make([]byte, maxSnippetBytes)
	n, err := reader.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// isSafePath ensures target is within cleanRoot and does not escape canonicalRoot.
func (s *RepositoryScanner) isSafePath(cleanRoot, canonicalRoot, target string) bool {
	cleanTarget := filepath.Clean(target)
	if !strings.HasPrefix(cleanTarget, cleanRoot) {
		return false
	}
	realTarget, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		return false
	}
	return strings.HasPrefix(realTarget, canonicalRoot)
}
