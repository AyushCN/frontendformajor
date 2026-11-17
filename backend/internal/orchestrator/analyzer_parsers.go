package orchestrator

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var (
	// pep503Regex replaces consecutive hyphens, underscores, and periods with a single hyphen.
	pep503Regex = regexp.MustCompile(`[-_.]+`)

	// pythonReqLineRegex extracts package name, optional extras, and version constraint.
	// Matches e.g.: "tensorflow>=2.17.0; sys_platform == 'linux'"
	pythonReqLineRegex = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]*\])?\s*([<>=!~].*)?$`)

	// pythonVersionSpecRegex matches version operators like ">=3.10", "==2.17.0".
	pythonVersionSpecRegex = regexp.MustCompile(`([<>=!~]{1,2})\s*([0-9A-Za-z.*+-]+)`)

	// gradleDepRegex matches implementation("group:artifact:version") or implementation 'group:artifact:version'
	gradleDepRegex = regexp.MustCompile(`(?:implementation|api|compileOnly|runtimeOnly|testImplementation)\s*[\(]?\s*['"]([^'":]+):([^'":]+)(?::([^'"]+))?['"]\s*[\)]?`)

	// pomArtifactRegex matches <artifactId>name</artifactId>
	pomArtifactRegex = regexp.MustCompile(`<artifactId>([^<]+)</artifactId>`)
	pomGroupRegex    = regexp.MustCompile(`<groupId>([^<]+)</groupId>`)
	pomVersionRegex  = regexp.MustCompile(`<version>([^<]+)</version>`)
)

// NormalizePythonPackage normalizes a Python package name per PEP 503.
// Comparisons across manifests MUST use this normalized name.
func NormalizePythonPackage(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return ""
	}
	clean = strings.ToLower(clean)
	return pep503Regex.ReplaceAllString(clean, "-")
}

// parseRequirementsTxt parses a standard requirements.txt file.
// Handles comments, options (-r, -i, -e), environment markers (; ...), and version bounds.
func parseRequirementsTxt(r io.Reader, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         ManifestRequirementsTxt,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		rawLine := strings.TrimSpace(scanner.Text())
		if rawLine == "" || strings.HasPrefix(rawLine, "#") {
			continue
		}

		// Inline comments
		if idx := strings.Index(rawLine, " #"); idx != -1 {
			rawLine = strings.TrimSpace(rawLine[:idx])
		}

		// Flags and includes (-r, -i, --extra-index-url, -f, etc.)
		if strings.HasPrefix(rawLine, "-") {
			manifest.RawEntries = append(manifest.RawEntries, rawLine)
			// Check for CUDA/GPU wheels index markers
			if strings.Contains(strings.ToLower(rawLine), "cu11") ||
				strings.Contains(strings.ToLower(rawLine), "cu12") ||
				strings.Contains(strings.ToLower(rawLine), "rocm") {
				manifest.Environment["cuda_index"] = rawLine
			}
			continue
		}

		// Environment marker handling (e.g. numpy>=2.0.0; python_version >= '3.10')
		marker := ""
		line := rawLine
		if parts := strings.SplitN(rawLine, ";", 2); len(parts) == 2 {
			line = strings.TrimSpace(parts[0])
			marker = strings.TrimSpace(parts[1])
			if strings.Contains(strings.ToLower(marker), "python_version") {
				manifest.Environment["marker:"+line] = marker
			}
		}

		// Parse package name and version
		matches := pythonReqLineRegex.FindStringSubmatch(line)
		if len(matches) < 2 {
			manifest.RawEntries = append(manifest.RawEntries, rawLine)
			continue
		}

		rawPkg := matches[1]
		normPkg := NormalizePythonPackage(rawPkg)
		if normPkg == "" {
			continue
		}

		versionSpec := ""
		if len(matches) >= 3 && matches[2] != "" {
			versionSpec = strings.TrimSpace(matches[2])
		}

		manifest.Dependencies[normPkg] = versionSpec
	}

	if err := scanner.Err(); err != nil {
		manifest.IsMalformed = true
		manifest.ParseError = err.Error()
	}

	return manifest
}

// parsePyprojectToml parses PEP 621, Poetry, or Flit declarations from pyproject.toml.
// Does safe line-oriented TOML scanning without heavy external dependencies.
func parsePyprojectToml(r io.Reader, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         ManifestPyprojectToml,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	currentSection := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Table header: [section]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.Trim(line, "[] \t"))
			continue
		}

		// Look for requires-python constraint
		if strings.HasPrefix(line, "requires-python") {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				val := cleanTomlString(parts[1])
				manifest.Environment["requires-python"] = val
			}
			continue
		}

		// Section: [project.dependencies] (array of string specs)
		if currentSection == "project.dependencies" || currentSection == "project" {
			if strings.HasPrefix(line, "dependencies") && strings.Contains(line, "=") {
				// Handled in array continuation or single line
				continue
			}
		}

		// In Poetry: [tool.poetry.dependencies] (key = value)
		if currentSection == "tool.poetry.dependencies" {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				val := cleanTomlString(parts[1])
				if strings.EqualFold(key, "python") {
					manifest.Environment["python"] = val
					continue
				}
				normPkg := NormalizePythonPackage(key)
				if normPkg != "" {
					manifest.Dependencies[normPkg] = val
				}
			}
			continue
		}

		// String item in an array: e.g. "tensorflow>=2.17.0",
		if strings.HasPrefix(line, "\"") || strings.HasPrefix(line, "'") {
			cleaned := cleanTomlString(line)
			matches := pythonReqLineRegex.FindStringSubmatch(cleaned)
			if len(matches) >= 2 {
				normPkg := NormalizePythonPackage(matches[1])
				versionSpec := ""
				if len(matches) >= 3 {
					versionSpec = strings.TrimSpace(matches[2])
				}
				if normPkg != "" {
					manifest.Dependencies[normPkg] = versionSpec
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		manifest.IsMalformed = true
		manifest.ParseError = err.Error()
	}

	return manifest
}

// parsePipfile parses Pipfile [packages] and [dev-packages] sections.
func parsePipfile(r io.Reader, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         ManifestPipfile,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	inPackages := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.ToLower(strings.Trim(line, "[] \t"))
			inPackages = (section == "packages" || section == "dev-packages")
			continue
		}

		if inPackages {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				pkg := strings.TrimSpace(parts[0])
				val := cleanTomlString(parts[1])
				normPkg := NormalizePythonPackage(pkg)
				if normPkg != "" {
					if val == "*" {
						val = ""
					}
					manifest.Dependencies[normPkg] = val
				}
			}
		}
	}

	return manifest
}

// parseLockfilePackages parses lockfiles with `[[package]]` entries (poetry.lock and uv.lock).
func parseLockfilePackages(r io.Reader, manifestType ManifestType, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         manifestType,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	var currentName, currentVer string

	commitPackage := func() {
		if currentName != "" {
			norm := NormalizePythonPackage(currentName)
			if norm != "" {
				manifest.Dependencies[norm] = "==" + currentVer
			}
		}
		currentName = ""
		currentVer = ""
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			commitPackage()
			continue
		}

		if strings.HasPrefix(line, "name =") {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				currentName = cleanTomlString(parts[1])
			}
		} else if strings.HasPrefix(line, "version =") {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				currentVer = cleanTomlString(parts[1])
			}
		}
	}
	commitPackage()

	return manifest
}

// parseSetupPyOrCfg scans setup.py or setup.cfg for install_requires without executing Python scripts.
func parseSetupPyOrCfg(r io.Reader, manifestType ManifestType, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         manifestType,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	inRequires := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		low := strings.ToLower(line)
		if strings.Contains(low, "install_requires") {
			inRequires = true
		}

		if inRequires {
			if strings.HasPrefix(line, "\"") || strings.HasPrefix(line, "'") {
				cleaned := cleanTomlString(line)
				matches := pythonReqLineRegex.FindStringSubmatch(cleaned)
				if len(matches) >= 2 {
					normPkg := NormalizePythonPackage(matches[1])
					versionSpec := ""
					if len(matches) >= 3 {
						versionSpec = strings.TrimSpace(matches[2])
					}
					if normPkg != "" {
						manifest.Dependencies[normPkg] = versionSpec
					}
				}
			}
			if strings.Contains(line, "]") || strings.Contains(line, ")") {
				inRequires = false
			}
		}
	}

	return manifest
}

// parsePackageJSON parses Node.js dependencies and engines from package.json.
func parsePackageJSON(r io.Reader, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         ManifestPackageJSON,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	var data struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Engines         map[string]string `json:"engines"`
	}

	dec := json.NewDecoder(r)
	if err := dec.Decode(&data); err != nil {
		manifest.IsMalformed = true
		manifest.ParseError = err.Error()
		return manifest
	}

	for k, v := range data.Dependencies {
		manifest.Dependencies[strings.ToLower(k)] = v
	}
	for k, v := range data.DevDependencies {
		// Do not overwrite production dependencies
		if _, exists := manifest.Dependencies[strings.ToLower(k)]; !exists {
			manifest.Dependencies[strings.ToLower(k)] = v
		}
	}
	for k, v := range data.Engines {
		manifest.Environment["engine:"+k] = v
	}

	return manifest
}

// parseGoMod parses module dependencies and Go toolchain version from go.mod.
func parseGoMod(r io.Reader, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         ManifestGoMod,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	inRequire := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		if strings.HasPrefix(line, "go ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				manifest.Environment["go_version"] = parts[1]
			}
			continue
		}

		if strings.HasPrefix(line, "require (") {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}

		if inRequire || strings.HasPrefix(line, "require ") {
			trimmed := strings.TrimPrefix(line, "require ")
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				modPath := parts[0]
				version := parts[1]
				manifest.Dependencies[modPath] = version
			}
		}
	}

	return manifest
}

// parseJavaManifest parses Gradle or Maven dependency declarations safely without toolchain invocation.
func parseJavaManifest(r io.Reader, mType ManifestType, manifestPath string) DependencyManifest {
	manifest := DependencyManifest{
		Type:         mType,
		RelativePath: manifestPath,
		Dependencies: make(map[string]string),
		Environment:  make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	var currentGroup, currentArtifact, currentVersion string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if mType == ManifestPomXML {
			if match := pomGroupRegex.FindStringSubmatch(line); len(match) > 1 {
				currentGroup = match[1]
			}
			if match := pomArtifactRegex.FindStringSubmatch(line); len(match) > 1 {
				currentArtifact = match[1]
			}
			if match := pomVersionRegex.FindStringSubmatch(line); len(match) > 1 {
				currentVersion = match[1]
			}
			if strings.Contains(line, "</dependency>") {
				if currentArtifact != "" {
					key := currentArtifact
					if currentGroup != "" {
						key = currentGroup + ":" + currentArtifact
					}
					manifest.Dependencies[key] = currentVersion
				}
				currentGroup = ""
				currentArtifact = ""
				currentVersion = ""
			}
		} else {
			// Gradle / Gradle KTS
			if matches := gradleDepRegex.FindStringSubmatch(line); len(matches) > 2 {
				group := matches[1]
				artifact := matches[2]
				version := ""
				if len(matches) > 3 {
					version = matches[3]
				}
				manifest.Dependencies[group+":"+artifact] = version
			}
		}
	}

	return manifest
}

// cleanTomlString strips quotes, trailing commas, and brackets from TOML/JSON values.
func cleanTomlString(s string) string {
	res := strings.TrimSpace(s)
	res = strings.Trim(res, "[]\"', \t")
	return res
}
