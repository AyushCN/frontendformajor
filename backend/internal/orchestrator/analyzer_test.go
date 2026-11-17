package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizePythonPackage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Flask", "flask"},
		{"scikit_learn", "scikit-learn"},
		{"Scikit-Learn", "scikit-learn"},
		{"scikit.learn", "scikit-learn"},
		{"ONNX_Runtime", "onnx-runtime"},
		{"tensorflow", "tensorflow"},
		{"PyTorch", "pytorch"},
		{"  requests  ", "requests"},
		{"", ""},
	}

	for _, tt := range tests {
		got := NormalizePythonPackage(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizePythonPackage(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestAnalyzer_RequirementsTxt_Classifications(t *testing.T) {
	tests := []struct {
		name             string
		requirements     string
		expectedCat      ProjectCategory
		expectedLanguage string
		hasCuda          bool
		containsSysLib   string
	}{
		{
			name: "Standard Flask Web App",
			requirements: `
# Web server
Flask>=3.0.0
gunicorn==21.2.0
requests>=2.31.0
`,
			expectedCat:      CategoryStandardPython,
			expectedLanguage: "python",
		},
		{
			name: "Data Science Only (No ML/DL)",
			requirements: `
numpy>=2.0.0
pandas>=2.2.0
statsmodels>=0.14.0
matplotlib>=3.8.0
`,
			expectedCat:      CategoryDataScience,
			expectedLanguage: "python",
		},
		{
			name: "Classical Machine Learning",
			requirements: `
pandas>=2.2.0
scikit-learn>=1.5.0
xgboost>=2.1.0
joblib>=1.4.0
`,
			expectedCat:      CategoryMachineLearning,
			expectedLanguage: "python",
			containsSysLib:   "libgomp1",
		},
		{
			name: "Deep Learning with TensorFlow",
			requirements: `
flask>=3.0.0
tensorflow>=2.17.0
pillow>=10.3.0
`,
			expectedCat:      CategoryDeepLearning,
			expectedLanguage: "python",
			containsSysLib:   "libjpeg-dev",
		},
		{
			name: "Deep Learning with PyTorch",
			requirements: `
torch>=2.4.0
torchvision>=0.19.0
`,
			expectedCat:      CategoryDeepLearning,
			expectedLanguage: "python",
			containsSysLib:   "libgomp1",
		},
		{
			name: "Explicit GPU Package",
			requirements: `
onnxruntime-gpu>=1.19.0
numpy>=1.26.0
`,
			expectedCat:      CategoryGPUML,
			expectedLanguage: "python",
			hasCuda:          true,
		},
		{
			name: "CUDA Wheel Index Flag",
			requirements: `
--extra-index-url https://download.pytorch.org/whl/cu121
torch
torchvision
`,
			expectedCat:      CategoryGPUML,
			expectedLanguage: "python",
			hasCuda:          true,
		},
	}

	analyzer := NewRepositoryAnalyzer()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			reqPath := filepath.Join(tmpDir, "requirements.txt")
			if err := os.WriteFile(reqPath, []byte(tt.requirements), 0o644); err != nil {
				t.Fatalf("failed to write requirements.txt: %v", err)
			}

			analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
			if err != nil {
				t.Fatalf("InspectRepository failed: %v", err)
			}

			if analysis.PrimaryLanguage != tt.expectedLanguage {
				t.Errorf("expected PrimaryLanguage=%q, got %q", tt.expectedLanguage, analysis.PrimaryLanguage)
			}
			if analysis.Category != tt.expectedCat {
				t.Errorf("expected Category=%q, got %q", tt.expectedCat, analysis.Category)
			}
			if analysis.HasCUDADependency != tt.hasCuda {
				t.Errorf("expected HasCUDADependency=%v, got %v", tt.hasCuda, analysis.HasCUDADependency)
			}
			if tt.containsSysLib != "" {
				found := false
				for _, lib := range analysis.RequiresSystemLibs {
					if lib == tt.containsSysLib {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected RequiresSystemLibs to contain %q, got %v", tt.containsSysLib, analysis.RequiresSystemLibs)
				}
			}
		})
	}
}

func TestAnalyzer_PyprojectToml(t *testing.T) {
	content := `
[project]
name = "demo-ml"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = [
    "scikit-learn>=1.5.0",
    "numpy>=1.26.0",
    "requests[security]>=2.31.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.0.0",
]
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write pyproject.toml: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.PrimaryLanguage != "python" {
		t.Errorf("expected python, got %q", analysis.PrimaryLanguage)
	}
	if analysis.Category != CategoryMachineLearning {
		t.Errorf("expected MACHINE_LEARNING, got %q", analysis.Category)
	}

	foundReqPython := false
	for _, m := range analysis.Manifests {
		if m.Environment["requires-python"] == ">=3.11" {
			foundReqPython = true
		}
	}
	if !foundReqPython {
		t.Errorf("expected requires-python >=3.11 in manifest environment")
	}
}

func TestAnalyzer_Pipfile(t *testing.T) {
	content := `
[[source]]
url = "https://pypi.org/simple"
verify_ssl = true
name = "pypi"

[packages]
flask = ">=3.0"
tensorflow = ">=2.17.0"

[dev-packages]
pytest = "*"
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "Pipfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write Pipfile: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.Category != CategoryDeepLearning {
		t.Errorf("expected DEEP_LEARNING, got %q", analysis.Category)
	}
}

func TestAnalyzer_PoetryLockAndUvLock(t *testing.T) {
	poetryContent := `
[[package]]
name = "fastapi"
version = "0.110.0"

[[package]]
name = "xgboost"
version = "2.1.0"
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "poetry.lock"), []byte(poetryContent), 0o644); err != nil {
		t.Fatalf("failed to write poetry.lock: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.Category != CategoryMachineLearning {
		t.Errorf("expected MACHINE_LEARNING, got %q", analysis.Category)
	}
}

func TestAnalyzer_SetupPyAndCfg(t *testing.T) {
	setupPy := `
from setuptools import setup

setup(
    name='sample_tool',
    version='1.0',
    install_requires=[
        'flask>=3.0',
        'pandas>=2.0.0',
    ],
)
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "setup.py"), []byte(setupPy), 0o644); err != nil {
		t.Fatalf("failed to write setup.py: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.Category != CategoryDataScience {
		t.Errorf("expected DATA_SCIENCE, got %q", analysis.Category)
	}
}

func TestAnalyzer_NodeJsPackageJSON(t *testing.T) {
	pkgJSON := `
{
  "name": "nextjs-app",
  "version": "1.0.0",
  "dependencies": {
    "next": "^14.2.0",
    "react": "^18.3.0"
  },
  "devDependencies": {
    "typescript": "^5.0.0"
  },
  "engines": {
    "node": ">=20.0.0"
  }
}
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.PrimaryLanguage != "nodejs" {
		t.Errorf("expected PrimaryLanguage=nodejs, got %q", analysis.PrimaryLanguage)
	}
	if analysis.Category != CategoryNodeJS {
		t.Errorf("expected Category=NODEJS, got %q", analysis.Category)
	}
}

func TestAnalyzer_GoMod(t *testing.T) {
	goMod := `
module github.com/example/api

go 1.25.0

require (
	github.com/go-chi/chi/v5 v5.0.12
	gorm.io/gorm v1.25.7
)
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.PrimaryLanguage != "golang" {
		t.Errorf("expected PrimaryLanguage=golang, got %q", analysis.PrimaryLanguage)
	}
	if analysis.Category != CategoryGolang {
		t.Errorf("expected Category=GOLANG, got %q", analysis.Category)
	}
}

func TestAnalyzer_JavaGradleAndMaven(t *testing.T) {
	pomXML := `
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <version>0.0.1-SNAPSHOT</version>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
      <version>3.2.0</version>
    </dependency>
  </dependencies>
</project>
`
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "pom.xml"), []byte(pomXML), 0o644); err != nil {
		t.Fatalf("failed to write pom.xml: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.PrimaryLanguage != "java" {
		t.Errorf("expected PrimaryLanguage=java, got %q", analysis.PrimaryLanguage)
	}
	if analysis.Category != CategoryJava {
		t.Errorf("expected Category=JAVA, got %q", analysis.Category)
	}
}

func TestAnalyzer_MultipleManifests_ConflictingVersions(t *testing.T) {
	tmpDir := t.TempDir()
	reqContent := "numpy>=2.0.0\nflask==3.0.0\n"
	pyprojectContent := `
[project]
dependencies = [
    "numpy>=1.26.0",
    "scikit-learn>=1.5.0",
]
`
	_ = os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte(reqContent), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "pyproject.toml"), []byte(pyprojectContent), 0o644)

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.Category != CategoryMachineLearning {
		t.Errorf("expected Category=MACHINE_LEARNING, got %q", analysis.Category)
	}

	// Should have flagged the conflict for numpy
	hasConflictWarning := false
	for _, w := range analysis.Warnings {
		if strings.Contains(w, "conflicting version constraint for numpy") {
			hasConflictWarning = true
			break
		}
	}
	if !hasConflictWarning {
		t.Errorf("expected warning about conflicting numpy constraint, got: %v", analysis.Warnings)
	}
}

func TestAnalyzer_MalformedManifest(t *testing.T) {
	tmpDir := t.TempDir()
	// Malformed JSON in package.json
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{ invalid json "), 0o644)

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository should not fail hard on malformed manifest: %v", err)
	}

	if len(analysis.Manifests) == 0 {
		t.Fatalf("expected at least 1 manifest recorded")
	}
	if !analysis.Manifests[0].IsMalformed {
		t.Errorf("expected manifest to be marked as malformed")
	}
}

func TestAnalyzer_SymlinkEscapeProtection(t *testing.T) {
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideSecret, []byte("SUPER_SECRET"), 0o644)

	repoDir := t.TempDir()
	// Create symlink pointing outside
	symlinkPath := filepath.Join(repoDir, "requirements.txt")
	if err := os.Symlink(outsideSecret, symlinkPath); err != nil {
		t.Skip("symlinks not supported on this filesystem")
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("InspectRepository error: %v", err)
	}

	// Symlink pointing outside must be skipped
	if len(analysis.Manifests) > 0 {
		t.Errorf("expected 0 manifests parsed from outside symlink, got %d", len(analysis.Manifests))
	}

	hasWarning := false
	for _, w := range analysis.Warnings {
		if strings.Contains(w, "skipping unsafe symlink") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Errorf("expected warning for unsafe symlink escape, got: %v", analysis.Warnings)
	}
}

func TestAnalyzer_IgnoredDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	// Place a fake requirements.txt in .git and node_modules
	_ = os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".git", "requirements.txt"), []byte("tensorflow>=2.0.0"), 0o644)

	_ = os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "node_modules", "requirements.txt"), []byte("torch>=2.0.0"), 0o644)

	_ = os.MkdirAll(filepath.Join(tmpDir, ".venv"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".venv", "requirements.txt"), []byte("scikit-learn"), 0o644)

	// Legitimate requirements in root
	_ = os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte("flask>=3.0.0"), 0o644)

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	// Should be STANDARD_PYTHON (flask), NOT deep learning or machine learning from ignored folders
	if analysis.Category != CategoryStandardPython {
		t.Errorf("expected STANDARD_PYTHON, got %q", analysis.Category)
	}
}

func TestAnalyzer_OriginalRepositoryImmutability(t *testing.T) {
	tmpDir := t.TempDir()
	reqPath := filepath.Join(tmpDir, "requirements.txt")
	originalContent := "flask==3.0.3\nrequests>=2.31.0\n"
	_ = os.WriteFile(reqPath, []byte(originalContent), 0o644)

	beforeStat, _ := os.Stat(reqPath)

	analyzer := NewRepositoryAnalyzer()
	_, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	afterStat, _ := os.Stat(reqPath)
	afterContent, _ := os.ReadFile(reqPath)

	if string(afterContent) != originalContent {
		t.Errorf("manifest content was modified during analysis!")
	}
	if beforeStat.ModTime() != afterStat.ModTime() {
		t.Errorf("file modification time changed during analysis!")
	}
}

func TestAnalyzer_DeterministicOutput(t *testing.T) {
	tmpDir := t.TempDir()
	reqContent := `
Flask>=3.0.3
numpy>=2.0.0
scikit-learn>=1.5.0
`
	_ = os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte(reqContent), 0o644)

	analyzer := NewRepositoryAnalyzer()

	res1, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("first inspection failed: %v", err)
	}

	res2, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("second inspection failed: %v", err)
	}

	if res1.PrimaryLanguage != res2.PrimaryLanguage {
		t.Errorf("non-deterministic PrimaryLanguage: %s vs %s", res1.PrimaryLanguage, res2.PrimaryLanguage)
	}
	if res1.Category != res2.Category {
		t.Errorf("non-deterministic Category: %s vs %s", res1.Category, res2.Category)
	}
	if !reflect.DeepEqual(res1.DiscoveredPackages, res2.DiscoveredPackages) {
		t.Errorf("non-deterministic DiscoveredPackages: %v vs %v", res1.DiscoveredPackages, res2.DiscoveredPackages)
	}
}

func TestAnalyzer_KrishiSenseFixture(t *testing.T) {
	krishiSenseReq := `# KrishiSense -- merged requirements
Flask>=3.0.3
numpy>=2.0.0
pandas>=2.2.0
scikit-learn>=1.5.0
joblib>=1.4.0
xgboost>=2.1.0
tensorflow>=2.17.0
statsmodels>=0.14.0
onnxruntime>=1.19.0
onnxmltools>=1.13.0
skl2onnx>=1.16.0
Pillow>=10.3.0
Werkzeug>=3.0.3
requests>=2.32.0
pyserial>=3.5
python-dotenv>=1.0.0
tqdm>=4.66.0
`
	tmpDir := t.TempDir()
	reqPath := filepath.Join(tmpDir, "requirements.txt")
	if err := os.WriteFile(reqPath, []byte(krishiSenseReq), 0o644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}

	analyzer := NewRepositoryAnalyzer()
	analysis, err := analyzer.InspectRepository(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}

	if analysis.PrimaryLanguage != "python" {
		t.Errorf("expected python, got %q", analysis.PrimaryLanguage)
	}
	// TensorFlow presence should classify as DEEP_LEARNING
	if analysis.Category != CategoryDeepLearning {
		t.Errorf("expected DEEP_LEARNING, got %q", analysis.Category)
	}
	if analysis.CategoryConfidence != ConfidenceHigh {
		t.Errorf("expected ConfidenceHigh, got %q", analysis.CategoryConfidence)
	}

	// Verify essential ML packages were extracted and normalized
	expectedPackages := []string{"flask", "numpy", "pandas", "scikit-learn", "xgboost", "tensorflow", "onnxruntime", "pillow"}
	pkgMap := make(map[string]bool)
	for _, p := range analysis.DiscoveredPackages {
		pkgMap[p] = true
	}
	for _, expected := range expectedPackages {
		if !pkgMap[expected] {
			t.Errorf("expected package %q in discovered packages, got: %v", expected, analysis.DiscoveredPackages)
		}
	}

	// Verify system libraries required (e.g. libgomp1 for xgboost/tensorflow, libjpeg-dev for pillow)
	sysMap := make(map[string]bool)
	for _, s := range analysis.RequiresSystemLibs {
		sysMap[s] = true
	}
	if !sysMap["libgomp1"] {
		t.Errorf("expected libgomp1 in required system libs, got %v", analysis.RequiresSystemLibs)
	}
	if !sysMap["libjpeg-dev"] {
		t.Errorf("expected libjpeg-dev in required system libs, got %v", analysis.RequiresSystemLibs)
	}

	// If live KrishiSense workdir exists on host, inspect it directly
	liveWorkDir := "/tmp/api-sandbox-links/work/krishisense-c83398"
	if _, err := os.Stat(liveWorkDir); err == nil {
		liveAnalysis, liveErr := analyzer.InspectRepository(context.Background(), liveWorkDir)
		if liveErr != nil {
			t.Fatalf("live InspectRepository failed: %v", liveErr)
		}
		if liveAnalysis.Category != CategoryDeepLearning {
			t.Errorf("live KrishiSense expected DEEP_LEARNING, got %q", liveAnalysis.Category)
		}
	}
}

