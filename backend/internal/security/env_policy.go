package security

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// templateSuffixes identifies files intended as non-secret templates or examples.
	templateSuffixes = []string{
		".example", ".sample", ".template", ".dist", ".default", ".schema",
	}

	// protectedEnvExact covers canonical sensitive environment configuration filenames.
	protectedEnvExact = map[string]bool{
		".env":                   true,
		".env.local":             true,
		".env.development.local": true,
		".env.dev.local":         true,
		".env.production":        true,
		".env.production.local":  true,
		".env.prod":              true,
		".env.prod.local":        true,
		".env.staging":           true,
		".env.staging.local":     true,
		".env.stage.local":       true,
		".env.test.local":        true,
	}

	// protectedEnvPattern matches filenames starting with .env. or ending with .env
	// excluding explicit templates.
	protectedEnvRegex = regexp.MustCompile(`(?i)^\.env(\.[a-z0-9_-]+)+$`)

	// Secret detection regexes for source code / Git commit scanning:
	privateKeyRegex    = regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	awsAccessKeyRegex  = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	awsSecretKeyRegex  = regexp.MustCompile(`(?i)aws[_\-]?secret[_\-]?access[_\-]?key\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})["']?`)
	githubTokenRegex   = regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,255}\b`)
	slackTokenRegex    = regexp.MustCompile(`xox[baprs]-[0-9]{12}-[0-9]{12}-[a-zA-Z0-9]{24}`)
	genericSecretRegex = regexp.MustCompile(`(?i)(api[_\-]?key|secret[_\-]?key|auth[_\-]?token|private[_\-]?key|database_password)\s*[:=]\s*["']([^"'\s]{8,})["']`)
	jwtRegex           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
)

// SecretFinding reports one detected credential in source files.
type SecretFinding struct {
	Type        string `json:"type"`
	Line        int    `json:"line"`
	Description string `json:"description"`
}

// IsProtectedEnvFile returns true if relPath corresponds to a protected environment file
// containing actual runtime secrets, and false for templates or non-env files.
func IsProtectedEnvFile(relPath string) bool {
	base := strings.ToLower(filepath.Base(relPath))

	// 1. Explicit templates are never protected
	if IsTemplateEnvFile(relPath) {
		return false
	}

	// 2. Canonical exact matches
	if protectedEnvExact[base] {
		return true
	}

	// 3. Any .env.*.local file
	if strings.HasPrefix(base, ".env.") && strings.HasSuffix(base, ".local") {
		return true
	}

	// 4. Matches general .env pattern without being a template
	if protectedEnvRegex.MatchString(base) {
		return true
	}

	return false
}

// IsTemplateEnvFile returns true if relPath is an example, sample, or documentation template.
func IsTemplateEnvFile(relPath string) bool {
	base := strings.ToLower(filepath.Base(relPath))

	// Must be an env-related file to be an env template
	if !strings.Contains(base, ".env") && !strings.Contains(base, "env.") {
		return false
	}

	for _, suffix := range templateSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}

	// Check words like example, sample, template anywhere in the name
	if strings.Contains(base, "example") || strings.Contains(base, "sample") ||
		strings.Contains(base, "template") || strings.Contains(base, "dist") {
		return true
	}

	return false
}

// MaskSecretValue produces an obfuscated representation for display in UI.
// For short secrets (<= 6 chars), returns fixed dots.
// For longer secrets, shows first 2 chars and masked remainder.
func MaskSecretValue(val string) string {
	if val == "" {
		return ""
	}
	if len(val) <= 6 {
		return "••••••••"
	}
	// Show at most 2 prefix characters, mask the rest
	prefix := val[:2]
	return prefix + strings.Repeat("•", 8)
}

// ScanContentForSecrets inspects a file content string for potential leaked secrets.
// Returns a list of findings without exposing the actual secret values.
func ScanContentForSecrets(content string) []SecretFinding {
	var findings []SecretFinding
	lines := strings.Split(content, "\n")

	for idx, line := range lines {
		lineNum := idx + 1
		trimmed := strings.TrimSpace(line)

		// Skip comments in source files
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		if privateKeyRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "PRIVATE_KEY",
				Line:        lineNum,
				Description: "Unencrypted Private Key block detected",
			})
		}
		if awsAccessKeyRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "AWS_ACCESS_KEY",
				Line:        lineNum,
				Description: "AWS Access Key ID detected",
			})
		}
		if awsSecretKeyRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "AWS_SECRET_KEY",
				Line:        lineNum,
				Description: "AWS Secret Access Key assignment detected",
			})
		}
		if githubTokenRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "GITHUB_TOKEN",
				Line:        lineNum,
				Description: "GitHub Personal Access Token or App Token detected",
			})
		}
		if slackTokenRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "SLACK_TOKEN",
				Line:        lineNum,
				Description: "Slack API Token detected",
			})
		}
		if genericSecretRegex.MatchString(line) {
			// Ignore common non-secret placeholder values
			low := strings.ToLower(line)
			if !strings.Contains(low, "your_") && !strings.Contains(low, "example") &&
				!strings.Contains(low, "placeholder") && !strings.Contains(low, "changeme") &&
				!strings.Contains(low, "test") && !strings.Contains(low, "dummy") &&
				!strings.Contains(low, "todo") {
				findings = append(findings, SecretFinding{
					Type:        "GENERIC_SECRET",
					Line:        lineNum,
					Description: "High-probability API key or credential assignment detected",
				})
			}
		}
		if jwtRegex.MatchString(line) {
			findings = append(findings, SecretFinding{
				Type:        "JWT_TOKEN",
				Line:        lineNum,
				Description: "Hardcoded JSON Web Token detected",
			})
		}
	}

	return findings
}
