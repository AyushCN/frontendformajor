package orchestrator

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// RuntimeResolutionSource tracks the exact origin of a runtime version decision.
type RuntimeResolutionSource string

const (
	SourceUserExplicit     RuntimeResolutionSource = "EXPLICIT_USER"
	SourceDockerfile       RuntimeResolutionSource = "DOCKERFILE"
	SourceRailpackJSON     RuntimeResolutionSource = "RAILPACK_CONFIG"
	SourceDotPythonVersion RuntimeResolutionSource = "DOT_PYTHON_VERSION"
	SourceRuntimeTxt       RuntimeResolutionSource = "RUNTIME_TXT"
	SourcePyProject        RuntimeResolutionSource = "PYPROJECT_TOML"
	SourceLockfile         RuntimeResolutionSource = "LOCKFILE"
	SourceManifest         RuntimeResolutionSource = "MANIFEST"
	SourceInferredML       RuntimeResolutionSource = "INFERRED_COMPATIBILITY"
	SourcePlatformDefault  RuntimeResolutionSource = "PLATFORM_DEFAULT"
)

// RuntimeResolution captures the outcome of the runtime version resolution.
type RuntimeResolution struct {
	Runtime          string                  `json:"runtime"`           // e.g. "python", "nodejs", "golang"
	Version          string                  `json:"version"`           // e.g. "3.11.9", "20.x"
	ExactVersion     string                  `json:"exact_version"`     // e.g. "3.11.9", "22.14.0", "1.24.0", "21.0.2"
	Status           VersionStatus           `json:"status"`            // VERSION_SUPPORTED, VERSION_PROVISIONING_REQUIRED, etc.
	Source           RuntimeResolutionSource `json:"source"`
	Confidence       ConfidenceLevel         `json:"confidence"`
	Constraints      []string                `json:"constraints"`       // Constraints considered during resolution
	Warnings         []string                `json:"warnings,omitempty"`
	Conflicts        []string                `json:"conflicts,omitempty"`
	RequiresOverride bool                    `json:"requires_override"` // True if explicit config conflicts with package needs
	TargetOS         string                  `json:"target_os,omitempty"`
	TargetArch       string                  `json:"target_arch,omitempty"`
	ArtifactID       string                  `json:"artifact_id,omitempty"`
	Strategy         string                  `json:"strategy,omitempty"` // "railpack" or "dockerfile"
	LegacyImage      string                  `json:"legacy_image,omitempty"`
}

// VersionRange defines a closed interval [MinMajor.MinMinor, MaxMajor.MaxMinor].
type VersionRange struct {
	MinMajor, MinMinor int
	MaxMajor, MaxMinor int
}

func (vr VersionRange) Contains(major, minor int) bool {
	if major < vr.MinMajor || major > vr.MaxMajor {
		return false
	}
	if major == vr.MinMajor && minor < vr.MinMinor {
		return false
	}
	if major == vr.MaxMajor && minor > vr.MaxMinor {
		return false
	}
	return true
}

func (vr VersionRange) Intersect(other VersionRange) (VersionRange, bool) {
	minMaj, minMin := vr.MinMajor, vr.MinMinor
	if other.MinMajor > minMaj || (other.MinMajor == minMaj && other.MinMinor > minMin) {
		minMaj, minMin = other.MinMajor, other.MinMinor
	}

	maxMaj, maxMin := vr.MaxMajor, vr.MaxMinor
	if other.MaxMajor < maxMaj || (other.MaxMajor == maxMaj && other.MaxMinor < maxMin) {
		maxMaj, maxMin = other.MaxMajor, other.MaxMinor
	}

	// Check validity
	if minMaj > maxMaj || (minMaj == maxMaj && minMin > maxMin) {
		return VersionRange{}, false
	}
	return VersionRange{
		MinMajor: minMaj, MinMinor: minMin,
		MaxMajor: maxMaj, MaxMinor: maxMin,
	}, true
}

func (vr VersionRange) String() string {
	return fmt.Sprintf("Python %d.%d - %d.%d", vr.MinMajor, vr.MinMinor, vr.MaxMajor, vr.MaxMinor)
}

// Known ML / Data Science compatibility rules with documented PyPI wheel matrices.
type packageCompatibilityRule struct {
	PackageName     string
	VersionSpec     string // Empty means general/default rule
	SupportedRange  VersionRange
	DocumentedBasis string
}

var (
	// Known platform candidates in preference order for ML ecosystems.
	// 3.11.9 is the preferred candidate when within range due to widespread binary wheels.
	supportedPythonCandidates = []struct {
		Version string
		Major   int
		Minor   int
	}{
		{"3.11.9", 3, 11},
		{"3.10.14", 3, 10},
		{"3.12.7", 3, 12},
		{"3.9.19", 3, 9},
	}

	// Traceable compatibility matrix for core ML/data-science packages.
	// Basis: Official PyPI manylinux wheel releases and project NEP-29 statements.
	mlCompatibilityRules = []packageCompatibilityRule{
		{
			PackageName:     "tensorflow",
			VersionSpec:     ">=2.16",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 9, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "TensorFlow 2.16-2.18 provides binary wheels for Python 3.9-3.12. No 3.13 wheels.",
		},
		{
			PackageName:     "tensorflow",
			VersionSpec:     "<2.16",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 11},
			DocumentedBasis: "TensorFlow <2.16 supports Python 3.8-3.11.",
		},
		{
			PackageName:     "torch",
			VersionSpec:     ">=2.4",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "PyTorch 2.4-2.5 release wheels target Python 3.8-3.12.",
		},
		{
			PackageName:     "torch",
			VersionSpec:     "<2.4",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 11},
			DocumentedBasis: "PyTorch <2.4 supports Python 3.8-3.11.",
		},
		{
			PackageName:     "torchvision",
			VersionSpec:     "",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "Torchvision wheels track PyTorch supported range (Python 3.8-3.12).",
		},
		{
			PackageName:     "torchaudio",
			VersionSpec:     "",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "Torchaudio wheels track PyTorch supported range (Python 3.8-3.12).",
		},
		{
			PackageName:     "scikit-learn",
			VersionSpec:     ">=1.5",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 10, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "Scikit-Learn 1.5 provides binary wheels for Python 3.10-3.12.",
		},
		{
			PackageName:     "scikit-learn",
			VersionSpec:     "<1.5",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 11},
			DocumentedBasis: "Scikit-Learn <1.5 supports Python 3.8-3.11.",
		},
		{
			PackageName:     "xgboost",
			VersionSpec:     ">=2.0",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "XGBoost 2.0+ supports Python 3.8-3.12.",
		},
		{
			PackageName:     "lightgbm",
			VersionSpec:     "",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "LightGBM official wheels support Python 3.8-3.12.",
		},
		{
			PackageName:     "onnxruntime",
			VersionSpec:     ">=1.18",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "ONNX Runtime 1.18+ supports Python 3.8-3.12.",
		},
		{
			PackageName:     "onnxruntime-gpu",
			VersionSpec:     "",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "ONNX Runtime GPU supports Python 3.8-3.12.",
		},
		{
			PackageName:     "numpy",
			VersionSpec:     ">=2.0",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 9, MaxMajor: 3, MaxMinor: 13},
			DocumentedBasis: "NumPy 2.0+ supports Python 3.9-3.13.",
		},
		{
			PackageName:     "numpy",
			VersionSpec:     "<2.0",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 9, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "NumPy 1.25-1.26 supports Python 3.9-3.12.",
		},
		{
			PackageName:     "scipy",
			VersionSpec:     ">=1.14",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 10, MaxMajor: 3, MaxMinor: 13},
			DocumentedBasis: "SciPy 1.14+ supports Python 3.10-3.13.",
		},
		{
			PackageName:     "scipy",
			VersionSpec:     "<1.14",
			SupportedRange:  VersionRange{MinMajor: 3, MinMinor: 9, MaxMajor: 3, MaxMinor: 12},
			DocumentedBasis: "SciPy <1.14 supports Python 3.9-3.12.",
		},
	}
)

// RuntimeResolver resolves compatible language runtimes.
type RuntimeResolver struct {
	policy *LegacyRuntimePolicy
}

// NewRuntimeResolver creates a new RuntimeResolver.
func NewRuntimeResolver() *RuntimeResolver {
	return &RuntimeResolver{
		policy: NewLegacyRuntimePolicy(),
	}
}

// NewRuntimeResolverWithPolicy creates a RuntimeResolver with an explicit legacy runtime policy.
func NewRuntimeResolverWithPolicy(policy *LegacyRuntimePolicy) *RuntimeResolver {
	if policy == nil {
		policy = NewLegacyRuntimePolicy()
	}
	return &RuntimeResolver{
		policy: policy,
	}
}

// ResolveRuntime executes deterministic runtime version resolution following the approved precedence.
func (r *RuntimeResolver) ResolveRuntime(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (RuntimeResolution, error) {
	if analysis == nil {
		analysis = &RepositoryAnalysis{
			PrimaryLanguage: "unknown",
			Category:        CategoryUnknown,
		}
	}

	lang := analysis.PrimaryLanguage
	if userCfg != nil && strings.TrimSpace(userCfg.Language) != "" {
		lang = strings.ToLower(strings.TrimSpace(userCfg.Language))
	}

	var res RuntimeResolution
	var err error

	switch lang {
	case "python":
		res, err = r.resolvePythonRuntime(workDir, analysis, userCfg)
	case "nodejs":
		res, err = r.resolveNodeRuntime(workDir, analysis, userCfg)
	case "golang":
		res, err = r.resolveGoRuntime(workDir, analysis, userCfg)
	case "java":
		res, err = r.resolveJavaRuntime(workDir, analysis, userCfg)
	default:
		res = RuntimeResolution{
			Runtime:    lang,
			Version:    "",
			Source:     SourcePlatformDefault,
			Confidence: ConfidenceLow,
			Warnings:   []string{"Unrecognized primary language; falling back to builder default"},
		}
	}

	if err != nil {
		return res, err
	}

	r.finalizeResolution(&res)
	return res, nil
}

// resolvePythonRuntime enforces strict Railpack Python precedence:
// 1. Explicit user runtime selection
// 2. Explicit Railpack configuration (railpack.json)
// 3. .python-version
// 4. runtime.txt
// 5. pyproject.toml constraints (requires-python)
// 6. Lockfile / dependency constraints
// 7. Inferred compatible runtime
// 8. Platform default
func (r *RuntimeResolver) resolvePythonRuntime(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (RuntimeResolution, error) {
	res := RuntimeResolution{
		Runtime:     "python",
		Confidence:  ConfidenceHigh,
		Constraints: make([]string, 0),
		Warnings:    make([]string, 0),
		Conflicts:   make([]string, 0),
	}

	// Calculate dependency compatibility range
	depRange, hasDepRange, depConstraints := r.calculateDependencyRange(analysis)
	if hasDepRange {
		res.Constraints = append(res.Constraints, depConstraints...)
	}

	// 1. Explicit user runtime selection
	if userCfg != nil && strings.TrimSpace(userCfg.RuntimeVersion) != "" {
		version := cleanVersionString(userCfg.RuntimeVersion)
		res.Version = version
		res.Source = SourceUserExplicit
		res.Confidence = ConfidenceHigh

		// Check if user choice conflicts with dependency compatibility
		if hasDepRange {
			maj, min, ok := parseMajorMinor(version)
			if ok && !depRange.Contains(maj, min) {
				conflictMsg := fmt.Sprintf("Explicit Python version %s conflicts with dependency range %s", version, depRange.String())
				res.Conflicts = append(res.Conflicts, conflictMsg)
				res.Warnings = append(res.Warnings, fmt.Sprintf("Warning: %s. Build may fail if binary wheels are missing.", conflictMsg))
				res.RequiresOverride = true
			}
		}
		return res, nil
	}

	// 2. Explicit Railpack configuration (railpack.json packages.python)
	if rpVer := readRailpackJSONPython(workDir); rpVer != "" {
		res.Version = rpVer
		res.Source = SourceRailpackJSON
		res.Confidence = ConfidenceHigh
		return res, nil
	}

	// 3. .python-version
	if dotVer := readFirstLine(filepath.Join(workDir, ".python-version")); dotVer != "" {
		clean := cleanVersionString(dotVer)
		res.Version = clean
		res.Source = SourceDotPythonVersion
		res.Confidence = ConfidenceHigh

		if hasDepRange {
			maj, min, ok := parseMajorMinor(clean)
			if ok && !depRange.Contains(maj, min) {
				res.Warnings = append(res.Warnings, fmt.Sprintf(".python-version %s is outside recommended dependency range %s", clean, depRange.String()))
			}
		}
		return res, nil
	}

	// 4. runtime.txt
	if rtVer := readRuntimeTxt(filepath.Join(workDir, "runtime.txt")); rtVer != "" {
		res.Version = rtVer
		res.Source = SourceRuntimeTxt
		res.Confidence = ConfidenceHigh
		return res, nil
	}

	// 5. pyproject.toml runtime constraints (requires-python)
	if reqPython := r.findPyprojectRequiresPython(analysis); reqPython != "" {
		res.Constraints = append(res.Constraints, "requires-python: "+reqPython)
		if compatibleCandidate := r.matchCandidateWithConstraint(reqPython, depRange, hasDepRange); compatibleCandidate != "" {
			res.Version = compatibleCandidate
			res.Source = SourcePyProject
			res.Confidence = ConfidenceMedium
			return res, nil
		}
	}

	// 6 & 7. Inferred compatible runtime from dependencies
	if hasDepRange {
		// Select best candidate within depRange
		candidate := r.selectBestPythonCandidate(depRange)
		if candidate != "" {
			res.Version = candidate
			res.Source = SourceInferredML
			res.Confidence = ConfidenceHigh
			return res, nil
		}
		// If depRange is impossible (e.g. conflicting requirements)
		res.Conflicts = append(res.Conflicts, fmt.Sprintf("Dependency constraints cannot be reconciled: %s", strings.Join(depConstraints, ", ")))
		res.Warnings = append(res.Warnings, "No compatible Python runtime found for all declared dependencies")
	}

	// 8. Platform default
	// If project is classified as ML/Data Science but has no explicit range, default to 3.11.9.
	// Otherwise for standard Python, default to 3.12.7.
	if analysis.Category == CategoryDeepLearning || analysis.Category == CategoryMachineLearning || analysis.Category == CategoryGPUML {
		res.Version = "3.11.9"
		res.Source = SourceInferredML
		res.Confidence = ConfidenceMedium
		res.Warnings = append(res.Warnings, "Inferred Python 3.11.9 based on machine learning project classification")
	} else {
		res.Version = "3.12.7"
		res.Source = SourcePlatformDefault
		res.Confidence = ConfidenceLow
	}

	return res, nil
}

// resolveNodeRuntime handles Node.js version discovery.
func (r *RuntimeResolver) resolveNodeRuntime(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (RuntimeResolution, error) {
	res := RuntimeResolution{
		Runtime:     "nodejs",
		Confidence:  ConfidenceHigh,
		Constraints: make([]string, 0),
		Warnings:    make([]string, 0),
	}

	if userCfg != nil && strings.TrimSpace(userCfg.RuntimeVersion) != "" {
		res.Version = cleanVersionString(userCfg.RuntimeVersion)
		res.Source = SourceUserExplicit
		return res, nil
	}

	// .nvmrc or .node-version
	if nvmVer := readFirstLine(filepath.Join(workDir, ".nvmrc")); nvmVer != "" {
		res.Version = cleanVersionString(nvmVer)
		res.Source = SourceDotPythonVersion
		return res, nil
	}
	if nodeVer := readFirstLine(filepath.Join(workDir, ".node-version")); nodeVer != "" {
		res.Version = cleanVersionString(nodeVer)
		res.Source = SourceDotPythonVersion
		return res, nil
	}

	// package.json engines.node
	for _, m := range analysis.Manifests {
		if m.Type == ManifestPackageJSON && m.Environment["engine:node"] != "" {
			res.Version = m.Environment["engine:node"]
			res.Source = SourceManifest
			res.Confidence = ConfidenceMedium
			return res, nil
		}
	}

	res.Version = "20"
	res.Source = SourcePlatformDefault
	res.Confidence = ConfidenceLow
	return res, nil
}

// resolveGoRuntime handles Go toolchain version discovery.
func (r *RuntimeResolver) resolveGoRuntime(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (RuntimeResolution, error) {
	res := RuntimeResolution{
		Runtime:     "golang",
		Confidence:  ConfidenceHigh,
		Constraints: make([]string, 0),
		Warnings:    make([]string, 0),
	}

	if userCfg != nil && strings.TrimSpace(userCfg.RuntimeVersion) != "" {
		res.Version = cleanVersionString(userCfg.RuntimeVersion)
		res.Source = SourceUserExplicit
		return res, nil
	}

	// go.mod directive
	for _, m := range analysis.Manifests {
		if m.Type == ManifestGoMod && m.Environment["go_version"] != "" {
			res.Version = m.Environment["go_version"]
			res.Source = SourceManifest
			return res, nil
		}
	}

	res.Version = "1.25"
	res.Source = SourcePlatformDefault
	res.Confidence = ConfidenceLow
	return res, nil
}

// resolveJavaRuntime handles Java version discovery.
func (r *RuntimeResolver) resolveJavaRuntime(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (RuntimeResolution, error) {
	res := RuntimeResolution{
		Runtime:    "java",
		Confidence: ConfidenceMedium,
		Source:     SourcePlatformDefault,
		Version:    "17",
	}
	if userCfg != nil && strings.TrimSpace(userCfg.RuntimeVersion) != "" {
		res.Version = cleanVersionString(userCfg.RuntimeVersion)
		res.Source = SourceUserExplicit
		res.Confidence = ConfidenceHigh
	}
	return res, nil
}

// calculateDependencyRange evaluates all discovered packages against the ML compatibility matrix and computes their intersection.
func (r *RuntimeResolver) calculateDependencyRange(analysis *RepositoryAnalysis) (VersionRange, bool, []string) {
	if analysis == nil || len(analysis.DiscoveredPackages) == 0 {
		return VersionRange{}, false, nil
	}

	// Initial broad range [3.8, 3.13]
	currentRange := VersionRange{MinMajor: 3, MinMinor: 8, MaxMajor: 3, MaxMinor: 13}
	matched := false
	var constraints []string

	// Create map of package to version for quick matching
	pkgVersionMap := make(map[string]string)
	for _, m := range analysis.Manifests {
		for p, v := range m.Dependencies {
			pkgVersionMap[p] = v
		}
	}

	for _, pkg := range analysis.DiscoveredPackages {
		norm := NormalizePythonPackage(pkg)
		declaredVer := pkgVersionMap[norm]

		for _, rule := range mlCompatibilityRules {
			if rule.PackageName != norm {
				continue
			}

			// If rule specifies a version condition (e.g. ">=2.16"), check if declared version satisfies or is unspecified
			if rule.VersionSpec != "" && declaredVer != "" {
				if !matchesRuleVersion(declaredVer, rule.VersionSpec) {
					continue
				}
			}

			// Intersect with current range
			newRange, ok := currentRange.Intersect(rule.SupportedRange)
			if ok {
				currentRange = newRange
				matched = true
				constraints = append(constraints, fmt.Sprintf("%s (%s): %s", norm, rule.DocumentedBasis, rule.SupportedRange.String()))
			}
			break
		}
	}

	return currentRange, matched, constraints
}

// selectBestPythonCandidate selects the highest preferred candidate within range.
func (r *RuntimeResolver) selectBestPythonCandidate(vr VersionRange) string {
	for _, cand := range supportedPythonCandidates {
		if vr.Contains(cand.Major, cand.Minor) {
			return cand.Version
		}
	}
	return ""
}

// matchCandidateWithConstraint matches requires-python (e.g. ">=3.10", "~=3.11") against candidates.
func (r *RuntimeResolver) matchCandidateWithConstraint(spec string, depRange VersionRange, hasDepRange bool) string {
	cleanSpec := strings.TrimSpace(spec)
	for _, cand := range supportedPythonCandidates {
		if hasDepRange && !depRange.Contains(cand.Major, cand.Minor) {
			continue
		}
		if satisfiesPythonSpec(cand.Major, cand.Minor, cleanSpec) {
			return cand.Version
		}
	}
	return ""
}

func (r *RuntimeResolver) findPyprojectRequiresPython(analysis *RepositoryAnalysis) string {
	for _, m := range analysis.Manifests {
		if m.Type == ManifestPyprojectToml && m.Environment["requires-python"] != "" {
			return m.Environment["requires-python"]
		}
	}
	return ""
}

// matchesRuleVersion checks if a declared version specifier aligns with rule condition.
func matchesRuleVersion(declared, ruleSpec string) bool {
	if ruleSpec == "" || declared == "" {
		return true
	}
	// Basic major/minor threshold check
	numRegex := regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
	ruleNum := numRegex.FindString(ruleSpec)
	declNum := numRegex.FindString(declared)
	if ruleNum == "" || declNum == "" {
		return true
	}

	ruleVal, err1 := strconv.ParseFloat(ruleNum, 64)
	declVal, err2 := strconv.ParseFloat(declNum, 64)
	if err1 != nil || err2 != nil {
		return true
	}

	if strings.HasPrefix(ruleSpec, ">=") {
		return declVal >= ruleVal
	}
	if strings.HasPrefix(ruleSpec, "<") {
		return declVal < ruleVal
	}
	return true
}

// satisfiesPythonSpec evaluates basic Python specifiers (>=3.10, ~=3.11, ^3.11).
func satisfiesPythonSpec(major, minor int, spec string) bool {
	parts := strings.Split(spec, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, ">=") {
			val := strings.TrimPrefix(part, ">=")
			reqMaj, reqMin, ok := parseMajorMinor(val)
			if ok && (major < reqMaj || (major == reqMaj && minor < reqMin)) {
				return false
			}
		} else if strings.HasPrefix(part, "<") {
			val := strings.TrimPrefix(part, "<")
			reqMaj, reqMin, ok := parseMajorMinor(val)
			if ok && (major > reqMaj || (major == reqMaj && minor >= reqMin)) {
				return false
			}
		} else if strings.HasPrefix(part, "~=") || strings.HasPrefix(part, "^") {
			val := strings.TrimPrefix(strings.TrimPrefix(part, "~="), "^")
			reqMaj, reqMin, ok := parseMajorMinor(val)
			if ok && (major != reqMaj || minor < reqMin) {
				return false
			}
		}
	}
	return true
}

func parseMajorMinor(version string) (int, int, bool) {
	re := regexp.MustCompile(`([0-9]+)\.([0-9]+)`)
	matches := re.FindStringSubmatch(version)
	if len(matches) < 3 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(matches[1])
	min, err2 := strconv.Atoi(matches[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

func readFirstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

func readRuntimeTxt(path string) string {
	line := readFirstLine(path)
	if line == "" {
		return ""
	}
	// e.g. "python-3.11.9"
	if strings.HasPrefix(line, "python-") {
		return strings.TrimPrefix(line, "python-")
	}
	return cleanVersionString(line)
}

func readRailpackJSONPython(dir string) string {
	path := filepath.Join(dir, "railpack.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Simple scan for "packages": { "python": "3.11.x" }
	re := regexp.MustCompile(`"python"\s*:\s*"([^"]+)"`)
	matches := re.FindStringSubmatch(string(data))
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func cleanVersionString(s string) string {
	clean := strings.TrimSpace(s)
	clean = strings.TrimPrefix(clean, "v")
	clean = strings.TrimPrefix(clean, "python-")
	clean = strings.TrimPrefix(clean, "node-")
	return clean
}

// finalizeResolution canonicalizes exact version, validates against OS/arch and legacy policies,
// computes the immutable artifact identity, and determines the true VersionStatus.
func (r *RuntimeResolver) finalizeResolution(res *RuntimeResolution) {
	if res.Version == "" {
		res.Status = VersionSupported
		return
	}

	if res.ExactVersion == "" {
		res.ExactVersion = r.resolveExactVersion(res.Runtime, res.Version)
	}

	res.TargetOS = strings.ToLower(runtime.GOOS)
	res.TargetArch = NormalizeArch(runtime.GOARCH)

	// Validate target OS and architecture
	if valid, reason := ValidateArchOS(res.TargetOS, res.TargetArch); !valid {
		res.Status = VersionIncompatible
		res.Conflicts = append(res.Conflicts, reason)
		return
	}

	// Validate against legacy and EOL policies
	policy := r.policy
	if policy == nil {
		policy = NewLegacyRuntimePolicy()
	}

	status, strategy, legacyImgOrReason := policy.EvaluateVersion(res.Runtime, res.Version)
	res.Strategy = strategy
	res.LegacyImage = legacyImgOrReason

	if status == VersionUnsupported {
		res.Status = VersionUnsupported
		res.Conflicts = append(res.Conflicts, legacyImgOrReason)
		res.Warnings = append(res.Warnings, fmt.Sprintf("Unsupported %s version %s: %s", res.Runtime, res.Version, legacyImgOrReason))
		return
	}

	// If explicit user choice conflicts with dependency compatibility
	if res.RequiresOverride || (len(res.Conflicts) > 0 && res.Source == SourceUserExplicit) {
		res.Status = VersionIncompatible
		return
	}

	if strategy == "dockerfile" && legacyImgOrReason != "" {
		res.Status = VersionProvisioningRequired
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s is not supported by standard Railpack provider; using isolated legacy Dockerfile strategy with approved image %s", res.Runtime, res.Version, legacyImgOrReason))
	} else {
		res.Status = VersionSupported
	}

	key := DefaultRuntimeCacheKey(res.Runtime, res.ExactVersion, "", "")
	res.ArtifactID = key.Key()
}

// resolveExactVersion resolves canonical exact version requirements (e.g. Python 3.11.9, Node.js 22.14.0, Go 1.24.0, Java 21.0.2).
func (r *RuntimeResolver) resolveExactVersion(lang, ver string) string {
	clean := cleanVersionString(ver)
	switch strings.ToLower(lang) {
	case "python":
		maj, min, ok := parseMajorMinor(clean)
		if !ok {
			return clean
		}
		parts := strings.Split(clean, ".")
		if len(parts) >= 3 {
			return clean
		}
		switch fmt.Sprintf("%d.%d", maj, min) {
		case "3.11":
			return "3.11.9"
		case "3.10":
			return "3.10.14"
		case "3.12":
			return "3.12.7"
		case "3.9":
			return "3.9.19"
		case "3.8":
			return "3.8.18"
		case "3.7":
			return "3.7.17"
		case "3.6":
			return "3.6.15"
		case "3.5":
			return "3.5.10"
		case "3.4":
			return "3.4.10"
		case "2.7":
			return "2.7.18"
		default:
			return clean + ".0"
		}
	case "nodejs", "node":
		parts := strings.Split(clean, ".")
		if len(parts) >= 3 {
			return clean
		}
		maj, err := strconv.Atoi(parts[0])
		if err != nil {
			return clean
		}
		switch maj {
		case 22:
			return "22.14.0"
		case 20:
			return "20.18.0"
		case 18:
			return "18.20.5"
		case 16:
			return "16.20.2"
		case 14:
			return "14.21.3"
		default:
			return fmt.Sprintf("%d.0.0", maj)
		}
	case "golang", "go":
		if strings.HasSuffix(clean, ".x") {
			base := strings.TrimSuffix(clean, ".x")
			return base + ".0"
		}
		parts := strings.Split(clean, ".")
		if len(parts) >= 3 {
			return clean
		}
		if len(parts) == 2 {
			return clean + ".0"
		}
		return clean
	case "java":
		parts := strings.Split(clean, ".")
		if len(parts) >= 3 {
			return clean
		}
		maj, err := strconv.Atoi(parts[0])
		if err != nil {
			return clean
		}
		switch maj {
		case 21:
			return "21.0.2"
		case 17:
			return "17.0.10"
		case 23:
			return "23.0.1"
		case 11:
			return "11.0.22"
		default:
			return fmt.Sprintf("%d.0.0", maj)
		}
	default:
		return clean
	}
}
