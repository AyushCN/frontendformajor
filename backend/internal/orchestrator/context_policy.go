package orchestrator

const (
	// DefaultMaxContextBytes is the default cap for total staged build context (1 GB).
	DefaultMaxContextBytes = 1024 * 1024 * 1024

	// DefaultMaxSingleFileBytes is the default cap for an individual file (200 MB).
	DefaultMaxSingleFileBytes = 200 * 1024 * 1024

	// DefaultMaxFileCount is the maximum number of files allowed in a single context (50,000).
	DefaultMaxFileCount = 50000

	// DefaultMaxDepth is the maximum directory nesting depth allowed (16).
	DefaultMaxDepth = 16
)

// ContextPolicy defines the bounds, exclusion rules, and preservation directives for staging.
type ContextPolicy struct {
	MaxContextBytes    int64    `json:"max_context_bytes"`
	MaxSingleFileBytes int64    `json:"max_single_file_bytes"`
	MaxFileCount       int      `json:"max_file_count"`
	MaxDepth           int      `json:"max_depth"`
	ExcludedPatterns   []string `json:"excluded_patterns"`
	PreservedPatterns  []string `json:"preserved_patterns"`
	RespectIgnore      bool     `json:"respect_ignore"`
	RejectUnsafeLinks  bool     `json:"reject_unsafe_links"`
}

// DefaultContextPolicy returns safe, production-grade defaults.
func DefaultContextPolicy() ContextPolicy {
	return ContextPolicy{
		MaxContextBytes:    DefaultMaxContextBytes,
		MaxSingleFileBytes: DefaultMaxSingleFileBytes,
		MaxFileCount:       DefaultMaxFileCount,
		MaxDepth:           DefaultMaxDepth,
		RespectIgnore:      true,
		RejectUnsafeLinks:  true,
		ExcludedPatterns: []string{
			".git",
			".git/**",
			".venv",
			".venv/**",
			"venv",
			"venv/**",
			"__pycache__",
			"__pycache__/**",
			"*.pyc",
			"*.pyo",
			".pytest_cache",
			".pytest_cache/**",
			".mypy_cache",
			".mypy_cache/**",
			".ruff_cache",
			".ruff_cache/**",
			"node_modules",
			"node_modules/**",
			".DS_Store",
			"Thumbs.db",
		},
		PreservedPatterns: []string{
			"*.db",
			"*.sqlite",
			"*.sqlite3",
			"models/**",
			"data/**",
			"datasets/**",
			"*.onnx",
			"*.pt",
			"*.pth",
			"*.h5",
			"*.pkl",
			"*.csv",
			"*.parquet",
			"Dockerfile*",
			"dockerfile*",
			"railpack.json",
			"requirements.txt",
			"pyproject.toml",
			"package.json",
			"go.mod",
			"go.sum",
		},
	}
}

// ExcludedEntry captures why a path was omitted from the staged context.
type ExcludedEntry struct {
	RelativePath string `json:"relative_path"`
	Reason       string `json:"reason"` // "SAFE_DEFAULT", "IGNORE_FILE", "SPECIAL_FILE", "OVERSIZED", "UNSAFE_SYMLINK"
	Pattern      string `json:"pattern,omitempty"`
}

// StagingResult records the immutable telemetry and filesystem reference of a staged context.
type StagingResult struct {
	StagingDir        string          `json:"-"` // Not exposed over public APIs
	Fingerprint       string          `json:"fingerprint"`
	OriginalFileCount int             `json:"original_file_count"`
	StagedFileCount   int             `json:"staged_file_count"`
	OriginalSizeBytes int64           `json:"original_size_bytes"`
	StagedSizeBytes   int64           `json:"staged_size_bytes"`
	ExcludedEntries   []ExcludedEntry `json:"excluded_entries,omitempty"`
	Warnings          []string        `json:"warnings,omitempty"`
	Errors            []string        `json:"errors,omitempty"`
	CleanedUp         bool            `json:"cleaned_up"`
	cleanupFn         func() error
}

// Cleanup safely removes the temporary staging directory and releases its disk quota.
func (r *StagingResult) Cleanup() error {
	if r == nil || r.CleanedUp || r.cleanupFn == nil {
		return nil
	}
	r.CleanedUp = true
	return r.cleanupFn()
}
