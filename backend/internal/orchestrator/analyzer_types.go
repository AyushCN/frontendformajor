package orchestrator

import "time"

// ProjectCategory classifies the project's primary domain and complexity.
type ProjectCategory string

const (
	CategoryStandardPython  ProjectCategory = "STANDARD_PYTHON"
	CategoryDataScience     ProjectCategory = "DATA_SCIENCE"
	CategoryMachineLearning ProjectCategory = "MACHINE_LEARNING"
	CategoryDeepLearning    ProjectCategory = "DEEP_LEARNING"
	CategoryGPUML           ProjectCategory = "GPU_ML"
	CategoryNodeJS          ProjectCategory = "NODEJS"
	CategoryGolang          ProjectCategory = "GOLANG"
	CategoryJava            ProjectCategory = "JAVA"
	CategoryC               ProjectCategory = "C"
	CategoryCpp             ProjectCategory = "CPP"
	CategoryRust            ProjectCategory = "RUST"
	CategoryUnknown         ProjectCategory = "UNKNOWN"
)

// ConfidenceLevel indicates the certainty of the classification.
type ConfidenceLevel string

const (
	ConfidenceHigh   ConfidenceLevel = "HIGH"
	ConfidenceMedium ConfidenceLevel = "MEDIUM"
	ConfidenceLow    ConfidenceLevel = "LOW"
)

// ManifestType identifies the source format of dependency declarations.
type ManifestType string

const (
	ManifestRequirementsTxt ManifestType = "requirements.txt"
	ManifestPyprojectToml   ManifestType = "pyproject.toml"
	ManifestPipfile         ManifestType = "Pipfile"
	ManifestPoetryLock      ManifestType = "poetry.lock"
	ManifestUvLock          ManifestType = "uv.lock"
	ManifestSetupPy         ManifestType = "setup.py"
	ManifestSetupCfg        ManifestType = "setup.cfg"
	ManifestPackageJSON     ManifestType = "package.json"
	ManifestGoMod           ManifestType = "go.mod"
	ManifestPomXML          ManifestType = "pom.xml"
	ManifestBuildGradle     ManifestType = "build.gradle"
	ManifestBuildGradleKts  ManifestType = "build.gradle.kts"
)

// DependencyManifest records discovered package and toolchain declarations from a single manifest file.
type DependencyManifest struct {
	Type         ManifestType      `json:"type"`
	RelativePath string            `json:"relative_path"`
	Dependencies map[string]string `json:"dependencies"` // Normalized package name -> version constraint (e.g. ">=2.17.0")
	Environment  map[string]string `json:"environment"`  // Discovered toolchain constraints (e.g. "python": ">=3.10")
	RawEntries   []string          `json:"raw_entries,omitempty"`
	IsMalformed  bool              `json:"is_malformed,omitempty"`
	ParseError   string            `json:"parse_error,omitempty"`
}

// RepositoryAnalysis captures the static findings of the repository inspection.
type RepositoryAnalysis struct {
	PrimaryLanguage    string               `json:"primary_language"` // "python", "nodejs", "golang", "java", "unknown"
	Category           ProjectCategory      `json:"category"`
	CategoryConfidence ConfidenceLevel      `json:"category_confidence"`
	HasDockerfile      bool                 `json:"has_dockerfile"`
	DockerfilePath     string               `json:"dockerfile_path,omitempty"`
	Manifests          []DependencyManifest `json:"manifests"`
	DiscoveredPackages []string             `json:"discovered_packages"` // Unique sorted PEP 503 normalized package names
	HasCUDADependency  bool                 `json:"has_cuda_dependency"` // Indicates explicit GPU/CUDA package markers
	RequiresSystemLibs []string             `json:"requires_system_libs,omitempty"`
	Warnings           []string             `json:"warnings,omitempty"`
	AnalyzedAt         time.Time            `json:"analyzed_at"`
}
