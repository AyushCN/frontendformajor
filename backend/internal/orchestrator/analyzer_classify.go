package orchestrator

import (
	"sort"
)

var (
	// gpuPackages: Packages explicitly built for GPU acceleration.
	gpuPackages = map[string]bool{
		"onnxruntime-gpu": true,
		"cupy":            true,
		"cupy-cuda11x":    true,
		"cupy-cuda12x":    true,
		"tensorrt":        true,
		"cuml":            true,
		"cugraph":         true,
		"cudf":            true,
		"jax-cuda":        true,
	}

	// deepLearningPackages: Heavy neural network and deep learning frameworks.
	deepLearningPackages = map[string]bool{
		"tensorflow":     true,
		"tensorflow-cpu": true,
		"tensorflow-gpu": true,
		"torch":          true,
		"pytorch":        true,
		"torchvision":    true,
		"torchaudio":     true,
		"keras":          true,
		"jax":            true,
		"flax":           true,
		"transformers":   true,
		"diffusers":      true,
		"timm":           true,
	}

	// machineLearningPackages: Classical ML, gradient boosting, and ONNX engines.
	machineLearningPackages = map[string]bool{
		"scikit-learn": true,
		"sklearn":      true,
		"xgboost":      true,
		"lightgbm":     true,
		"catboost":     true,
		"onnx":         true,
		"onnxruntime":  true,
		"onnxmltools":  true,
		"skl2onnx":     true,
		"optuna":       true,
		"hyperopt":     true,
		"spacy":        true,
		"gensim":       true,
	}

	// dataSciencePackages: Numerical, scientific, tabular and plotting libraries.
	// NOTE: Merely having pandas/numpy does NOT promote a project to Machine Learning or Deep Learning.
	dataSciencePackages = map[string]bool{
		"numpy":       true,
		"pandas":      true,
		"scipy":       true,
		"statsmodels": true,
		"polars":      true,
		"dask":        true,
		"xarray":      true,
		"sympy":       true,
		"matplotlib":  true,
		"seaborn":     true,
		"plotly":      true,
		"bokeh":       true,
	}

	// systemLibRequirements maps specific Python packages to their required Linux system packages.
	systemLibRequirements = map[string][]string{
		"opencv-python":             {"libgl1", "libglib2.0-0"},
		"opencv-python-headless":    {"libglib2.0-0"},
		"pillow":                    {"libjpeg-dev", "zlib1g-dev"},
		"psycopg2":                  {"libpq-dev"},
		"pycurl":                    {"libcurl4-openssl-dev", "libssl-dev"},
		"scipy":                     {"libopenblas-dev", "gfortran"},
		"xgboost":                   {"libgomp1"},
		"lightgbm":                  {"libgomp1"},
		"tensorflow":                {"libgomp1"},
		"torch":                     {"libgomp1"},
		"lxml":                      {"libxml2-dev", "libxslt1-dev"},
		"cryptography":              {"libffi-dev", "libssl-dev"},
		"pyaudio":                   {"portaudio19-dev"},
	}
)

// classifyPythonProject determines the ProjectCategory and ConfidenceLevel based on aggregated package names.
func classifyPythonProject(packages map[string]string, hasCudaIndex bool) (ProjectCategory, ConfidenceLevel, bool, []string) {
	hasGPU := hasCudaIndex
	hasDL := false
	hasML := false
	hasDS := false
	totalDeps := len(packages)

	sysLibsSet := make(map[string]bool)

	for pkg := range packages {
		norm := NormalizePythonPackage(pkg)

		// Check system library requirements
		if libs, exists := systemLibRequirements[norm]; exists {
			for _, lib := range libs {
				sysLibsSet[lib] = true
			}
		}

		if gpuPackages[norm] {
			hasGPU = true
		} else if deepLearningPackages[norm] {
			hasDL = true
		} else if machineLearningPackages[norm] {
			hasML = true
		} else if dataSciencePackages[norm] {
			hasDS = true
		}
	}

	var sysLibs []string
	for lib := range sysLibsSet {
		sysLibs = append(sysLibs, lib)
	}
	sort.Strings(sysLibs)

	// Strict classification hierarchy:
	// GPU_ML > DEEP_LEARNING > MACHINE_LEARNING > DATA_SCIENCE > STANDARD_PYTHON
	if hasGPU {
		return CategoryGPUML, ConfidenceHigh, true, sysLibs
	}
	if hasDL {
		return CategoryDeepLearning, ConfidenceHigh, false, sysLibs
	}
	if hasML {
		return CategoryMachineLearning, ConfidenceHigh, false, sysLibs
	}
	if hasDS {
		return CategoryDataScience, ConfidenceHigh, false, sysLibs
	}

	if totalDeps > 0 {
		return CategoryStandardPython, ConfidenceHigh, false, sysLibs
	}

	return CategoryStandardPython, ConfidenceMedium, false, sysLibs
}

// deduplicateAndSortPackages returns sorted, unique, normalized package names.
func deduplicateAndSortPackages(packages map[string]string) []string {
	var list []string
	for pkg := range packages {
		norm := NormalizePythonPackage(pkg)
		if norm != "" {
			list = append(list, norm)
		}
	}
	sort.Strings(list)

	// Deduplicate
	var unique []string
	for i, p := range list {
		if i == 0 || p != list[i-1] {
			unique = append(unique, p)
		}
	}
	return unique
}
