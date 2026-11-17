package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeResolver_Precedence(t *testing.T) {
	// Setup repo directory with multiple version sources:
	// 1. pyproject.toml says requires-python = ">=3.9"
	// 2. runtime.txt says python-3.10.14
	// 3. .python-version says 3.11.9
	// 4. railpack.json says packages.python = "3.12.7"
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte("[project]\nrequires-python = '>=3.9'\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "runtime.txt"), []byte("python-3.10.14"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, ".python-version"), []byte("3.11.9"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "railpack.json"), []byte(`{"packages":{"python":"3.12.7"}}`), 0o644)

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("analyzer failed: %v", err)
	}

	resolver := NewRuntimeResolver()

	// Case 1: Explicit user configuration wins over all repo files
	userCfg := &UserBuildConfig{RuntimeVersion: "3.9.19"}
	res, err := resolver.ResolveRuntime(tmpDir, analysis, userCfg)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Version != "3.9.19" || res.Source != SourceUserExplicit {
		t.Errorf("expected explicit user version 3.9.19, got %s (source: %s)", res.Version, res.Source)
	}

	// Case 2: Without userCfg, railpack.json takes precedence over .python-version
	res, err = resolver.ResolveRuntime(tmpDir, analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Version != "3.12.7" || res.Source != SourceRailpackJSON {
		t.Errorf("expected railpack.json version 3.12.7, got %s (source: %s)", res.Version, res.Source)
	}

	// Case 3: Remove railpack.json -> .python-version wins over runtime.txt
	_ = os.Remove(filepath.Join(tmpDir, "railpack.json"))
	res, err = resolver.ResolveRuntime(tmpDir, analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Version != "3.11.9" || res.Source != SourceDotPythonVersion {
		t.Errorf("expected .python-version 3.11.9, got %s (source: %s)", res.Version, res.Source)
	}

	// Case 4: Remove .python-version -> runtime.txt wins
	_ = os.Remove(filepath.Join(tmpDir, ".python-version"))
	res, err = resolver.ResolveRuntime(tmpDir, analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Version != "3.10.14" || res.Source != SourceRuntimeTxt {
		t.Errorf("expected runtime.txt 3.10.14, got %s (source: %s)", res.Version, res.Source)
	}
}

func TestRuntimeResolver_TensorFlowCompatibility(t *testing.T) {
	// Repository has TensorFlow >= 2.17.0 (supports Python 3.9 - 3.12; NOT 3.13)
	// and Scikit-Learn >= 1.5.0 (supports Python 3.10 - 3.12)
	analysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryDeepLearning,
		DiscoveredPackages: []string{"tensorflow", "scikit-learn"},
		Manifests: []DependencyManifest{
			{
				Type: ManifestRequirementsTxt,
				Dependencies: map[string]string{
					"tensorflow":   ">=2.17.0",
					"scikit-learn": ">=1.5.0",
				},
			},
		},
	}

	resolver := NewRuntimeResolver()
	res, err := resolver.ResolveRuntime("/tmp/fake", analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	// Should infer 3.11.9 (highest preferred candidate within [3.10, 3.12])
	if res.Version != "3.11.9" {
		t.Errorf("expected inferred version 3.11.9, got %s", res.Version)
	}
	if res.Source != SourceInferredML {
		t.Errorf("expected SourceInferredML, got %s", res.Source)
	}
	if res.Confidence != ConfidenceHigh {
		t.Errorf("expected ConfidenceHigh, got %s", res.Confidence)
	}

	// User requests Python 3.13 explicitly -> Conflict must be reported!
	userCfg := &UserBuildConfig{RuntimeVersion: "3.13.1"}
	resConflict, err := resolver.ResolveRuntime("/tmp/fake", analysis, userCfg)
	if err != nil {
		t.Fatalf("resolve with conflict should not crash: %v", err)
	}
	// User explicit version is preserved, but conflict is surfaced
	if resConflict.Version != "3.13.1" {
		t.Errorf("user explicit version must not be overridden silently; got %s", resConflict.Version)
	}
	if len(resConflict.Conflicts) == 0 {
		t.Errorf("expected conflict reported when explicit 3.13 is paired with tensorflow")
	}
	if !resConflict.RequiresOverride {
		t.Errorf("expected RequiresOverride=true for explicit conflict")
	}
}

func TestRuntimeResolver_PyTorchCompatibility(t *testing.T) {
	// PyTorch >= 2.4.0 (Python 3.8 - 3.12)
	analysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryDeepLearning,
		DiscoveredPackages: []string{"torch", "torchvision"},
		Manifests: []DependencyManifest{
			{
				Type: ManifestRequirementsTxt,
				Dependencies: map[string]string{
					"torch":       ">=2.4.0",
					"torchvision": ">=0.19.0",
				},
			},
		},
	}

	resolver := NewRuntimeResolver()
	res, err := resolver.ResolveRuntime("/tmp/fake", analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if res.Version != "3.11.9" {
		t.Errorf("expected 3.11.9, got %s", res.Version)
	}
	if res.Source != SourceInferredML {
		t.Errorf("expected SourceInferredML, got %s", res.Source)
	}
}

func TestRuntimeResolver_StandardPythonDefault(t *testing.T) {
	// Standard Flask app with no ML dependencies
	analysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryStandardPython,
		DiscoveredPackages: []string{"flask", "gunicorn", "requests"},
		Manifests: []DependencyManifest{
			{
				Type: ManifestRequirementsTxt,
				Dependencies: map[string]string{
					"flask":    ">=3.0.0",
					"gunicorn": ">=21.0.0",
					"requests": ">=2.31.0",
				},
			},
		},
	}

	resolver := NewRuntimeResolver()
	res, err := resolver.ResolveRuntime("/tmp/fake", analysis, nil)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	// For standard Python with no ML dependencies, platform default is 3.12.7
	if res.Version != "3.12.7" {
		t.Errorf("expected 3.12.7, got %s", res.Version)
	}
	if res.Source != SourcePlatformDefault {
		t.Errorf("expected SourcePlatformDefault, got %s", res.Source)
	}
}

func TestRuntimeResolver_NodeAndGo(t *testing.T) {
	resolver := NewRuntimeResolver()

	// Node.js
	analysisNode := &RepositoryAnalysis{
		PrimaryLanguage: "nodejs",
		Category:        CategoryNodeJS,
		Manifests: []DependencyManifest{
			{
				Type:        ManifestPackageJSON,
				Environment: map[string]string{"engine:node": ">=18.0.0"},
			},
		},
	}
	resNode, err := resolver.ResolveRuntime("/tmp/fake", analysisNode, nil)
	if err != nil {
		t.Fatalf("node resolve failed: %v", err)
	}
	if resNode.Version != ">=18.0.0" {
		t.Errorf("expected >=18.0.0, got %s", resNode.Version)
	}

	// Go
	analysisGo := &RepositoryAnalysis{
		PrimaryLanguage: "golang",
		Category:        CategoryGolang,
		Manifests: []DependencyManifest{
			{
				Type:        ManifestGoMod,
				Environment: map[string]string{"go_version": "1.25.0"},
			},
		},
	}
	resGo, err := resolver.ResolveRuntime("/tmp/fake", analysisGo, nil)
	if err != nil {
		t.Fatalf("go resolve failed: %v", err)
	}
	if resGo.Version != "1.25.0" {
		t.Errorf("expected 1.25.0, got %s", resGo.Version)
	}
}

func TestRuntimeResolver_Deterministic(t *testing.T) {
	analysis := &RepositoryAnalysis{
		PrimaryLanguage:    "python",
		Category:           CategoryMachineLearning,
		DiscoveredPackages: []string{"scikit-learn", "xgboost", "numpy"},
		Manifests: []DependencyManifest{
			{
				Type: ManifestRequirementsTxt,
				Dependencies: map[string]string{
					"scikit-learn": ">=1.5.0",
					"xgboost":      ">=2.1.0",
					"numpy":        ">=2.0.0",
				},
			},
		},
	}

	resolver := NewRuntimeResolver()
	r1, err1 := resolver.ResolveRuntime("/tmp/fake", analysis, nil)
	r2, err2 := resolver.ResolveRuntime("/tmp/fake", analysis, nil)
	if err1 != nil || err2 != nil {
		t.Fatalf("resolution failed: %v / %v", err1, err2)
	}

	if r1.Version != r2.Version || r1.Source != r2.Source || len(r1.Constraints) != len(r2.Constraints) {
		t.Errorf("non-deterministic runtime resolution: %+v vs %+v", r1, r2)
	}
}
