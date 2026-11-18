package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuilderSelection records the builder choice and the justification.
type BuilderSelection struct {
	SelectedBuilder string `json:"selected_builder"`        // "dockerfile", "railpack", "nixpacks"
	SelectionSource string `json:"builder_selection_source"`// "USER_EXPLICIT", "AUTOMATIC_DOCKERFILE", "AUTOMATIC_RAILPACK"
	DockerfilePath  string `json:"dockerfile_path,omitempty"`
}

// DockerfileRecommendation provides actionable guidance when project complexity warrants a containerfile.
type DockerfileRecommendation struct {
	Recommended     bool     `json:"recommended"`
	Reason          string   `json:"reason,omitempty"`
	SuggestedBase   string   `json:"suggested_base,omitempty"`
	RequiredSysLibs []string `json:"required_sys_libs,omitempty"`
	RequiresGPU     bool     `json:"requires_gpu,omitempty"`
	TriggerPackages []string `json:"trigger_packages,omitempty"`
}

// BuilderStrategySelector selects the build engine and generates architectural recommendations.
type BuilderStrategySelector struct{}

// NewBuilderStrategySelector creates a new BuilderStrategySelector.
func NewBuilderStrategySelector() *BuilderStrategySelector {
	return &BuilderStrategySelector{}
}

// SelectBuilder determines which builder engine to use based on strict precedence:
// 1. Explicit user builder selection
// 2. Automatic Dockerfile detection (root Dockerfile or configured path)
// 3. Legacy runtime strategy routing (if runtime requires isolated Dockerfile)
// 4. Railpack automatic fallback
func (s *BuilderStrategySelector) SelectBuilder(workDir string, analysis *RepositoryAnalysis, userCfg *UserBuildConfig) (BuilderSelection, error) {
	return s.SelectBuilderWithRuntime(workDir, analysis, userCfg, nil)
}

// SelectBuilderWithRuntime incorporates runtime resolution outcomes (including legacy policies) into builder selection.
func (s *BuilderStrategySelector) SelectBuilderWithRuntime(
	workDir string,
	analysis *RepositoryAnalysis,
	userCfg *UserBuildConfig,
	runtimeRes *RuntimeResolution,
) (BuilderSelection, error) {
	effectiveDir := workDir
	if userCfg != nil && strings.TrimSpace(userCfg.RootDir) != "" {
		clean := filepath.Clean(strings.TrimSpace(userCfg.RootDir))
		if strings.HasPrefix(clean, "..") {
			return BuilderSelection{}, fmt.Errorf("builder strategy: invalid root_dir %q traverses outside repository", userCfg.RootDir)
		}
		effectiveDir = filepath.Join(workDir, clean)
	}

	// 1. Explicit user builder selection
	if userCfg != nil && strings.TrimSpace(userCfg.Builder) != "" && strings.ToLower(strings.TrimSpace(userCfg.Builder)) != "auto" {
		chosen := strings.ToLower(strings.TrimSpace(userCfg.Builder))
		switch chosen {
		case "dockerfile":
			dfPath, err := s.resolveDockerfile(effectiveDir, userCfg.DockerfilePath)
			if err != nil {
				return BuilderSelection{}, fmt.Errorf("builder strategy: %w", err)
			}
			return BuilderSelection{
				SelectedBuilder: "dockerfile",
				SelectionSource: "USER_EXPLICIT",
				DockerfilePath:  dfPath,
			}, nil
		case "railpack":
			return BuilderSelection{
				SelectedBuilder: "railpack",
				SelectionSource: "USER_EXPLICIT",
			}, nil
		case "nixpacks":
			return BuilderSelection{
				SelectedBuilder: "nixpacks",
				SelectionSource: "USER_EXPLICIT",
			}, nil
		default:
			return BuilderSelection{}, fmt.Errorf("builder strategy: unsupported builder engine %q (supported: dockerfile, railpack, nixpacks)", userCfg.Builder)
		}
	}

	// 2. Automatic Dockerfile detection
	// Check user-configured DockerfilePath first
	if userCfg != nil && strings.TrimSpace(userCfg.DockerfilePath) != "" {
		dfPath, err := s.resolveDockerfile(effectiveDir, userCfg.DockerfilePath)
		if err != nil {
			return BuilderSelection{}, fmt.Errorf("builder strategy: %w", err)
		}
		return BuilderSelection{
			SelectedBuilder: "dockerfile",
			SelectionSource: "AUTOMATIC_DOCKERFILE",
			DockerfilePath:  dfPath,
		}, nil
	}

	// Check repository root for Dockerfile
	if dfPath, err := findDockerfileInDir(effectiveDir); err == nil {
		return BuilderSelection{
			SelectedBuilder: "dockerfile",
			SelectionSource: "AUTOMATIC_DOCKERFILE",
			DockerfilePath:  dfPath,
		}, nil
	}

	// 3. Isolated legacy runtime build strategy routing
	// If the runtime requires a legacy container strategy (e.g. Python 3.5-3.9, Node 14-16)
	if runtimeRes != nil && runtimeRes.Strategy == "dockerfile" && runtimeRes.Status != VersionUnsupported {
		return BuilderSelection{
			SelectedBuilder: "dockerfile",
			SelectionSource: "LEGACY_RUNTIME_STRATEGY",
		}, nil
	}
	if userCfg != nil && userCfg.RuntimeVersion != "" {
		policy := NewLegacyRuntimePolicy()
		lang := "python"
		if userCfg.Language != "" {
			lang = userCfg.Language
		} else if analysis != nil && analysis.PrimaryLanguage != "" {
			lang = analysis.PrimaryLanguage
		}
		status, strategy, _ := policy.EvaluateVersion(lang, userCfg.RuntimeVersion)
		if strategy == "dockerfile" && status != VersionUnsupported {
			return BuilderSelection{
				SelectedBuilder: "dockerfile",
				SelectionSource: "LEGACY_RUNTIME_STRATEGY",
			}, nil
		}
	}

	// 4. Supported Railpack automatic fallback
	return BuilderSelection{
		SelectedBuilder: "railpack",
		SelectionSource: "AUTOMATIC_RAILPACK",
	}, nil
}

// EvaluateDockerfileRecommendation analyzes repository complexity and formulates an advisory recommendation.
// Invariant: A recommendation NEVER silently changes the selected builder.
func (s *BuilderStrategySelector) EvaluateDockerfileRecommendation(analysis *RepositoryAnalysis, selectedBuilder string) DockerfileRecommendation {
	if analysis == nil {
		return DockerfileRecommendation{Recommended: false}
	}

	// If the user is already building with a Dockerfile, no recommendation is needed
	if selectedBuilder == "dockerfile" {
		return DockerfileRecommendation{Recommended: false}
	}

	// Trigger 1: Explicit CUDA / GPU package declarations
	if analysis.HasCUDADependency || analysis.Category == CategoryGPUML {
		return DockerfileRecommendation{
			Recommended:     true,
			Reason:          "Repository declares GPU/CUDA dependencies. Rootless BuildKit / Railpack environments do not provide GPU drivers or CUDA compilation toolchains. A specialized Dockerfile with NVIDIA base layers is recommended.",
			SuggestedBase:   "nvidia/cuda:12.2.0-runtime-ubuntu22.04",
			RequiresGPU:     true,
			RequiredSysLibs: analysis.RequiresSystemLibs,
			TriggerPackages: filterTriggerPackages(analysis.DiscoveredPackages, gpuPackages),
		}
	}

	// Trigger 2: Deep Learning frameworks requiring system libraries (OpenMP, Fortran, JPEG, etc.)
	if analysis.Category == CategoryDeepLearning {
		triggerPkgs := filterTriggerPackages(analysis.DiscoveredPackages, deepLearningPackages)
		return DockerfileRecommendation{
			Recommended:     true,
			Reason:          fmt.Sprintf("Repository declares deep learning frameworks (%s). Pre-baked Dockerfiles prevent multi-gigabyte wheel downloads during deployment and provide required native libraries.", strings.Join(triggerPkgs, ", ")),
			SuggestedBase:   "python:3.11-slim",
			RequiresGPU:     false,
			RequiredSysLibs: analysis.RequiresSystemLibs,
			TriggerPackages: triggerPkgs,
		}
	}

	// Trigger 3: Machine Learning with native compilation requirements
	if analysis.Category == CategoryMachineLearning && len(analysis.RequiresSystemLibs) > 0 {
		triggerPkgs := filterTriggerPackages(analysis.DiscoveredPackages, machineLearningPackages)
		return DockerfileRecommendation{
			Recommended:     true,
			Reason:          fmt.Sprintf("Repository declares machine learning dependencies (%s) requiring Linux system libraries (%s). A Dockerfile enables explicit system package installation (apt-get) and predictable layer caching.", strings.Join(triggerPkgs, ", "), strings.Join(analysis.RequiresSystemLibs, ", ")),
			SuggestedBase:   "python:3.11-slim",
			RequiresGPU:     false,
			RequiredSysLibs: analysis.RequiresSystemLibs,
			TriggerPackages: triggerPkgs,
		}
	}

	return DockerfileRecommendation{
		Recommended: false,
	}
}

// resolveDockerfile validates and canonicalizes a Dockerfile path within baseDir.
func (s *BuilderStrategySelector) resolveDockerfile(baseDir, configuredPath string) (string, error) {
	cleanPath := strings.TrimSpace(configuredPath)
	if cleanPath == "" {
		return findDockerfileInDir(baseDir)
	}

	target := cleanPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, cleanPath)
	}

	// Validate path does not escape baseDir
	cleanBase, err1 := filepath.Abs(baseDir)
	cleanTarget, err2 := filepath.Abs(target)
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("resolving path: %s", target)
	}

	if !strings.HasPrefix(cleanTarget, cleanBase) {
		return "", fmt.Errorf("custom Dockerfile path %q traverses outside repository root", configuredPath)
	}

	fi, err := os.Stat(cleanTarget)
	if err != nil || fi.IsDir() {
		return "", fmt.Errorf("Dockerfile not found at %s", cleanTarget)
	}

	return cleanTarget, nil
}

func findDockerfileInDir(dir string) (string, error) {
	for _, name := range []string{"Dockerfile", "dockerfile"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Dockerfile found in %s", dir)
}

func filterTriggerPackages(discovered []string, matchMap map[string]bool) []string {
	var matched []string
	for _, p := range discovered {
		if matchMap[p] {
			matched = append(matched, p)
		}
	}
	return matched
}
