package orchestrator

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreRule represents a single compiled ignore or negation pattern.
type IgnoreRule struct {
	Pattern   string
	IsNegated bool
	Source    string // "DEFAULT", "DOCKERIGNORE", "RAILPACKIGNORE", "PRESERVE"
}

// IgnoreEngine evaluates whether a file or directory should be excluded or preserved.
type IgnoreEngine struct {
	rules         []IgnoreRule
	preserveRules []IgnoreRule
}

// NewIgnoreEngine compiles default exclusions, preservation rules, and builder-specific ignore files.
func NewIgnoreEngine(baseDir, builder string, policy *ContextPolicy) *IgnoreEngine {
	engine := &IgnoreEngine{
		rules:         make([]IgnoreRule, 0),
		preserveRules: make([]IgnoreRule, 0),
	}

	if policy == nil {
		def := DefaultContextPolicy()
		policy = &def
	}

	// 1. Compile safe default exclusions
	for _, pat := range policy.ExcludedPatterns {
		engine.addRule(pat, "DEFAULT")
	}

	// 2. Compile preservation directives
	for _, pat := range policy.PreservedPatterns {
		pat = strings.TrimSpace(pat)
		if pat != "" {
			engine.preserveRules = append(engine.preserveRules, IgnoreRule{
				Pattern: normalizeGlobPattern(pat),
				Source:  "PRESERVE",
			})
		}
	}

	// 3. Parse builder-specific ignore files if enabled
	if policy.RespectIgnore {
		if builder == "dockerfile" {
			engine.loadIgnoreFile(filepath.Join(baseDir, ".dockerignore"), "DOCKERIGNORE")
		} else {
			engine.loadIgnoreFile(filepath.Join(baseDir, ".railpackignore"), "RAILPACKIGNORE")
			// If .railpackignore not found, fall back to .dockerignore if present
			if len(engine.rules) == len(policy.ExcludedPatterns) {
				engine.loadIgnoreFile(filepath.Join(baseDir, ".dockerignore"), "DOCKERIGNORE")
			}
		}
	}

	return engine
}

// ShouldExclude determines whether relPath should be excluded from staging.
// Returns (excluded, reason, pattern).
func (e *IgnoreEngine) ShouldExclude(relPath string, isDir bool) (bool, string, string) {
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	if cleanRel == "." || cleanRel == "" {
		return false, "", ""
	}

	// First: Check if this file is explicitly preserved by preservation policy
	isPreserved := false
	var preservePattern string
	for _, pr := range e.preserveRules {
		if matchPattern(pr.Pattern, cleanRel, isDir) {
			isPreserved = true
			preservePattern = pr.Pattern
			break
		}
	}

	// Second: Evaluate ignore rules in order (later rules override earlier rules)
	excluded := false
	reason := ""
	lastPattern := ""

	for _, rule := range e.rules {
		if matchPattern(rule.Pattern, cleanRel, isDir) {
			if rule.IsNegated {
				excluded = false
				reason = ""
				lastPattern = ""
			} else {
				excluded = true
				reason = rule.Source
				lastPattern = rule.Pattern
			}
		}
	}

	// If matched an exclusion rule, but it was on the preservation list:
	// Only user-supplied .dockerignore or .railpackignore can override preservation.
	// Default exclusions (e.g. *.db or broad *.cache) cannot override preservation.
	if excluded && isPreserved && reason == "DEFAULT" {
		return false, "", preservePattern
	}

	return excluded, reason, lastPattern
}

func (e *IgnoreEngine) addRule(line, source string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}

	isNegated := false
	if strings.HasPrefix(line, "!") {
		isNegated = true
		line = strings.TrimPrefix(line, "!")
		line = strings.TrimSpace(line)
	}

	pattern := normalizeGlobPattern(line)
	if pattern != "" {
		e.rules = append(e.rules, IgnoreRule{
			Pattern:   pattern,
			IsNegated: isNegated,
			Source:    source,
		})
	}
}

func (e *IgnoreEngine) loadIgnoreFile(path, source string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := io.LimitReader(f, 1024*1024) // 1MB max ignore file
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		e.addRule(scanner.Text(), source)
	}
}

func normalizeGlobPattern(pat string) string {
	pat = filepath.ToSlash(strings.TrimSpace(pat))
	pat = strings.TrimPrefix(pat, "./")
	pat = strings.TrimPrefix(pat, "/")
	return pat
}

// matchPattern checks if relPath matches a Dockerignore / glob pattern.
func matchPattern(pattern, relPath string, isDir bool) bool {
	if pattern == "" || relPath == "" {
		return false
	}

	// If pattern ends with '/', it only matches directories or files inside it
	dirOnly := strings.HasSuffix(pattern, "/")
	cleanPattern := strings.TrimSuffix(pattern, "/")

	// Exact match
	if cleanPattern == relPath {
		return true
	}

	// Subtree wildcard: "dir/**" or "dir/*"
	if strings.HasSuffix(cleanPattern, "/**") {
		basePrefix := strings.TrimSuffix(cleanPattern, "/**")
		if relPath == basePrefix || strings.HasPrefix(relPath, basePrefix+"/") {
			return true
		}
	}

	// Leading directory match (e.g. pattern "node_modules" matches "node_modules/foo/bar")
	if strings.HasPrefix(relPath, cleanPattern+"/") {
		return true
	}

	// Validate pattern before matching to reject malformed globs (e.g. [invalid).
	// Also reject patterns that are purely wildcards with no specificity (e.g. ***),
	// which would match every file and are almost certainly mistakes.
	if _, err := filepath.Match(cleanPattern, ""); err != nil {
		// Malformed pattern — skip silently.
		return false
	}
	// Reject degenerate all-wildcard patterns (e.g. "***", "****").
	allStars := true
	for _, c := range cleanPattern {
		if c != '*' {
			allStars = false
			break
		}
	}
	if allStars && len(cleanPattern) >= 3 {
		return false
	}

	// Standard filepath.Match
	matched, err := filepath.Match(cleanPattern, relPath)
	if err == nil && matched {
		if dirOnly && !isDir {
			return false
		}
		return true
	}

	// Match against base name (e.g. pattern "*.pyc" matches "subdir/module.pyc")
	baseName := filepath.Base(relPath)
	matchedBase, err := filepath.Match(cleanPattern, baseName)
	if err == nil && matchedBase {
		if dirOnly && !isDir {
			return false
		}
		return true
	}

	return false
}
