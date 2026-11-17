package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrategySelector_ExplicitBuilder(t *testing.T) {
	selector := NewBuilderStrategySelector()
	tmpDir := t.TempDir()

	// 1. Explicit Railpack selection
	userCfg := &UserBuildConfig{Builder: "railpack"}
	sel, err := selector.SelectBuilder(tmpDir, nil, userCfg)
	if err != nil {
		t.Fatalf("explicit railpack failed: %v", err)
	}
	if sel.SelectedBuilder != "railpack" || sel.SelectionSource != "USER_EXPLICIT" {
		t.Errorf("expected explicit railpack, got %+v", sel)
	}

	// 2. Explicit Dockerfile selection without Dockerfile -> must return structured error
	userCfg = &UserBuildConfig{Builder: "dockerfile"}
	_, err = selector.SelectBuilder(tmpDir, nil, userCfg)
	if err == nil {
		t.Fatalf("expected error when explicit Dockerfile is requested but absent")
	}

	// 3. Create Dockerfile -> explicit Dockerfile succeeds
	dfPath := filepath.Join(tmpDir, "Dockerfile")
	_ = os.WriteFile(dfPath, []byte("FROM alpine\n"), 0o644)
	sel, err = selector.SelectBuilder(tmpDir, nil, userCfg)
	if err != nil {
		t.Fatalf("explicit dockerfile should succeed with Dockerfile present: %v", err)
	}
	if sel.SelectedBuilder != "dockerfile" || sel.SelectionSource != "USER_EXPLICIT" {
		t.Errorf("expected explicit dockerfile, got %+v", sel)
	}

	// 4. Unsupported builder -> must return structured error
	userCfg = &UserBuildConfig{Builder: "unsupported-engine"}
	_, err = selector.SelectBuilder(tmpDir, nil, userCfg)
	if err == nil || !strings.Contains(err.Error(), "unsupported builder engine") {
		t.Fatalf("expected unsupported builder engine error, got: %v", err)
	}
}

func TestStrategySelector_AutomaticDetection(t *testing.T) {
	selector := NewBuilderStrategySelector()
	tmpDir := t.TempDir()

	// Case 1: No Dockerfile -> automatic Railpack fallback
	sel, err := selector.SelectBuilder(tmpDir, nil, nil)
	if err != nil {
		t.Fatalf("automatic selection failed: %v", err)
	}
	if sel.SelectedBuilder != "railpack" || sel.SelectionSource != "AUTOMATIC_RAILPACK" {
		t.Errorf("expected automatic railpack, got %+v", sel)
	}

	// Case 2: Root Dockerfile present -> automatic Dockerfile precedence
	_ = os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte("FROM python:3.11\n"), 0o644)
	sel, err = selector.SelectBuilder(tmpDir, nil, nil)
	if err != nil {
		t.Fatalf("automatic selection with Dockerfile failed: %v", err)
	}
	if sel.SelectedBuilder != "dockerfile" || sel.SelectionSource != "AUTOMATIC_DOCKERFILE" {
		t.Errorf("expected automatic dockerfile, got %+v", sel)
	}
}

func TestStrategySelector_CustomDockerfilePath_PathTraversal(t *testing.T) {
	selector := NewBuilderStrategySelector()
	repoDir := t.TempDir()

	// Valid nested Dockerfile
	subDir := filepath.Join(repoDir, "docker")
	_ = os.MkdirAll(subDir, 0o755)
	validDf := filepath.Join(subDir, "Dockerfile.custom")
	_ = os.WriteFile(validDf, []byte("FROM alpine\n"), 0o644)

	userCfg := &UserBuildConfig{DockerfilePath: "docker/Dockerfile.custom"}
	sel, err := selector.SelectBuilder(repoDir, nil, userCfg)
	if err != nil {
		t.Fatalf("valid custom Dockerfile failed: %v", err)
	}
	if sel.SelectedBuilder != "dockerfile" || sel.DockerfilePath != validDf {
		t.Errorf("expected custom dockerfile at %s, got %+v", validDf, sel)
	}

	// Malicious path traversal outside repo root
	userCfg = &UserBuildConfig{DockerfilePath: "../../../etc/passwd"}
	_, err = selector.SelectBuilder(repoDir, nil, userCfg)
	if err == nil || !strings.Contains(err.Error(), "traverses outside repository root") {
		t.Errorf("expected path traversal rejection error, got: %v", err)
	}
}

func TestStrategySelector_DockerfileRecommendations(t *testing.T) {
	selector := NewBuilderStrategySelector()

	// 1. Standard Flask App -> No recommendation
	stdAnalysis := &RepositoryAnalysis{
		PrimaryLanguage: "python",
		Category:        CategoryStandardPython,
	}
	recStd := selector.EvaluateDockerfileRecommendation(stdAnalysis, "railpack")
	if recStd.Recommended {
		t.Errorf("expected no Dockerfile recommendation for standard web app")
	}

	// 2. CUDA / GPU Project -> Strong recommendation with RequiresGPU
	gpuAnalysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryGPUML,
		HasCUDADependency:  true,
		DiscoveredPackages: []string{"onnxruntime-gpu", "numpy"},
	}
	recGPU := selector.EvaluateDockerfileRecommendation(gpuAnalysis, "railpack")
	if !recGPU.Recommended || !recGPU.RequiresGPU {
		t.Errorf("expected GPU Dockerfile recommendation with RequiresGPU=true, got %+v", recGPU)
	}

	// 3. Deep Learning (TensorFlow) -> Recommendation for native pre-baked image
	dlAnalysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryDeepLearning,
		DiscoveredPackages: []string{"tensorflow", "pillow"},
		RequiresSystemLibs: []string{"libgomp1", "libjpeg-dev"},
	}
	recDL := selector.EvaluateDockerfileRecommendation(dlAnalysis, "railpack")
	if !recDL.Recommended || recDL.RequiresGPU {
		t.Errorf("expected DeepLearning Dockerfile recommendation with RequiresGPU=false, got %+v", recDL)
	}
	if len(recDL.RequiredSysLibs) == 0 {
		t.Errorf("expected required system libs in recommendation")
	}

	// 4. Invariant: If builder is ALREADY Dockerfile, recommendation is false
	recAlreadyDF := selector.EvaluateDockerfileRecommendation(dlAnalysis, "dockerfile")
	if recAlreadyDF.Recommended {
		t.Errorf("expected recommendation=false when builder is already dockerfile")
	}
}
