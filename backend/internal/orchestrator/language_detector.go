package orchestrator

import (
	"context"
	"sort"
	"strings"
)

// DetectedLanguage captures evidence and confidence for a single language presence in the repository.
type DetectedLanguage struct {
	Language    string          `json:"language"`
	Confidence  ConfidenceLevel `json:"confidence"`
	Evidence    []string        `json:"evidence"`
	SourceCount int             `json:"source_count"`
}

// LanguageDetectionResult represents the multi-language detection analysis of a repository.
type LanguageDetectionResult struct {
	PrimaryLanguage   string             `json:"primary_language"`
	DetectedLanguages []DetectedLanguage `json:"detected_languages"`
	IsMixedLanguage   bool               `json:"is_mixed_language"`
}

// LanguageDetector inspects scanned repository contents to classify supported languages.
type LanguageDetector struct{}

// NewLanguageDetector creates a new LanguageDetector.
func NewLanguageDetector() *LanguageDetector {
	return &LanguageDetector{}
}

// DetectLanguages evaluates language evidence in strict precedence:
// 1. Explicit user configuration
// 2. Repository build configuration and manifests
// 3. Source file extensions
// 4. Static source inspection
func (d *LanguageDetector) DetectLanguages(
	ctx context.Context,
	repo *ScannedRepository,
	userCfg *UserBuildConfig,
) *LanguageDetectionResult {
	res := &LanguageDetectionResult{
		PrimaryLanguage:   "unknown",
		DetectedLanguages: make([]DetectedLanguage, 0),
	}

	if repo == nil {
		return res
	}

	// 1. Precedence 1: Explicit user configuration
	if userCfg != nil && strings.TrimSpace(userCfg.Language) != "" {
		userLang := strings.ToLower(strings.TrimSpace(userCfg.Language))
		primary := DetectedLanguage{
			Language:    userLang,
			Confidence:  ConfidenceHigh,
			Evidence:    []string{"explicit user configuration: " + userLang},
			SourceCount: len(repo.Files),
		}
		res.PrimaryLanguage = userLang
		res.DetectedLanguages = append(res.DetectedLanguages, primary)
		return res
	}

	// Tally discovered languages
	langEvidence := make(map[string][]string)
	langSources := make(map[string]int)

	// 2. Precedence 2: Repository build configuration and manifests
	for _, m := range repo.Manifests {
		lower := strings.ToLower(filepathBase(m))
		switch lower {
		case "requirements.txt", "pyproject.toml", "pipfile":
			langEvidence["python"] = append(langEvidence["python"], "manifest: "+m)
		case "package.json":
			langEvidence["nodejs"] = append(langEvidence["nodejs"], "manifest: "+m)
		case "go.mod":
			langEvidence["golang"] = append(langEvidence["golang"], "manifest: "+m)
		case "pom.xml", "build.gradle", "build.gradle.kts":
			langEvidence["java"] = append(langEvidence["java"], "manifest: "+m)
		case "cargo.toml":
			langEvidence["rust"] = append(langEvidence["rust"], "manifest: "+m)
		}
	}

	for _, b := range repo.BuildConfigs {
		lower := strings.ToLower(filepathBase(b))
		switch lower {
		case "makefile", "cmakelists.txt":
			// If Makefile or CMakeLists exists, add evidence for C/C++ if relevant files exist
			if len(repo.FilesByExt[".c"]) > 0 {
				langEvidence["c"] = append(langEvidence["c"], "build configuration: "+b)
			}
			if len(repo.FilesByExt[".cpp"]) > 0 || len(repo.FilesByExt[".cc"]) > 0 || len(repo.FilesByExt[".cxx"]) > 0 {
				langEvidence["cpp"] = append(langEvidence["cpp"], "build configuration: "+b)
			}
		}
	}

	// 3. Precedence 3: Source file extensions
	extMap := map[string]string{
		".py":  "python",
		".c":   "c",
		".h":   "c",
		".cpp": "cpp",
		".cc":  "cpp",
		".cxx": "cpp",
		".hpp": "cpp",
		".js":  "nodejs",
		".ts":  "nodejs",
		".mjs": "nodejs",
		".cjs": "nodejs",
		".go":  "golang",
		".java": "java",
		".rs":  "rust",
	}

	for ext, files := range repo.FilesByExt {
		if lang, ok := extMap[ext]; ok {
			langSources[lang] += len(files)
			evidenceMsg := "source files (" + ext + ")"
			if !containsString(langEvidence[lang], evidenceMsg) {
				langEvidence[lang] = append(langEvidence[lang], evidenceMsg)
			}
		}
	}

	// Build sorted detected languages
	for lang, count := range langSources {
		evidence := langEvidence[lang]
		conf := ConfidenceMedium
		if len(evidence) > 1 || count > 0 {
			conf = ConfidenceHigh
		}
		res.DetectedLanguages = append(res.DetectedLanguages, DetectedLanguage{
			Language:    lang,
			Confidence:  conf,
			Evidence:    evidence,
			SourceCount: count,
		})
	}

	// Also include languages with manifest evidence even if 0 sources matched yet
	for lang, evidence := range langEvidence {
		if langSources[lang] == 0 {
			res.DetectedLanguages = append(res.DetectedLanguages, DetectedLanguage{
				Language:    lang,
				Confidence:  ConfidenceHigh,
				Evidence:    evidence,
				SourceCount: 0,
			})
		}
	}

	if len(res.DetectedLanguages) > 1 {
		res.IsMixedLanguage = true
	}

	// Sort languages by source count descending, then manifest evidence
	sort.SliceStable(res.DetectedLanguages, func(i, j int) bool {
		if res.DetectedLanguages[i].SourceCount != res.DetectedLanguages[j].SourceCount {
			return res.DetectedLanguages[i].SourceCount > res.DetectedLanguages[j].SourceCount
		}
		return len(res.DetectedLanguages[i].Evidence) > len(res.DetectedLanguages[j].Evidence)
	})

	if len(res.DetectedLanguages) > 0 {
		res.PrimaryLanguage = res.DetectedLanguages[0].Language
	}

	return res
}

func filepathBase(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
