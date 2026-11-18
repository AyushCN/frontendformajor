package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// LegacyRuntimePolicy manages compatibility policies, approved legacy images, and unsupported version enforcement.
type LegacyRuntimePolicy struct {
	mu             sync.RWMutex
	allowLegacy    bool
	approvedImages map[string]string // "python:3.5" -> "python:3.5-slim"
}

// NewLegacyRuntimePolicy returns an initialized LegacyRuntimePolicy.
func NewLegacyRuntimePolicy() *LegacyRuntimePolicy {
	return &LegacyRuntimePolicy{
		allowLegacy: true,
		approvedImages: map[string]string{
			"python:3.5": "python:3.5-slim",
			"python:3.6": "python:3.6-slim",
			"python:3.7": "python:3.7-slim",
			"python:3.8": "python:3.8-slim",
			"python:3.9": "python:3.9-slim",
			"nodejs:14":  "node:14-slim",
			"nodejs:16":  "node:16-slim",
			"node:14":    "node:14-slim",
			"node:16":    "node:16-slim",
			"java:11":    "openjdk:11-jre-slim",
		},
	}
}

// SetAllowLegacy enables or disables the use of legacy runtime images based on security policy.
func (p *LegacyRuntimePolicy) SetAllowLegacy(allow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowLegacy = allow
}

// IsAllowLegacy returns whether legacy runtime images are currently permitted.
func (p *LegacyRuntimePolicy) IsAllowLegacy() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowLegacy
}

// ApproveImage adds or updates an approved legacy image mapping.
func (p *LegacyRuntimePolicy) ApproveImage(languageVersion, imageRef string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approvedImages[strings.ToLower(strings.TrimSpace(languageVersion))] = strings.TrimSpace(imageRef)
}

// RevokeImage removes an approved legacy image.
func (p *LegacyRuntimePolicy) RevokeImage(languageVersion string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.approvedImages, strings.ToLower(strings.TrimSpace(languageVersion)))
}

// IsApproved checks if an image is approved for the given language version.
func (p *LegacyRuntimePolicy) IsApproved(languageVersion string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, found := p.approvedImages[strings.ToLower(strings.TrimSpace(languageVersion))]
	return found
}

// GetApprovedImage returns the approved container image reference for a given language version.
func (p *LegacyRuntimePolicy) GetApprovedImage(languageVersion string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.approvedImages[strings.ToLower(strings.TrimSpace(languageVersion))]
}

// ApprovedImages returns a copy of all approved legacy images.
func (p *LegacyRuntimePolicy) ApprovedImages() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make(map[string]string, len(p.approvedImages))
	for k, v := range p.approvedImages {
		cp[k] = v
	}
	return cp
}

// EvaluateVersion checks if a language version is supported natively by Railpack,
// requires an approved legacy container strategy, or is unsupported/unsupported-EOL.
// Returns (status, strategy, approvedImageOrReason).
func (p *LegacyRuntimePolicy) EvaluateVersion(language, version string) (VersionStatus, string, string) {
	lang := strings.ToLower(strings.TrimSpace(language))
	ver := strings.TrimSpace(version)
	if ver == "" {
		return VersionSupported, "railpack", ""
	}

	switch lang {
	case "python":
		return p.evaluatePythonVersion(ver)
	case "nodejs", "node":
		return p.evaluateNodeVersion(ver)
	case "golang", "go":
		return p.evaluateGoVersion(ver)
	case "java":
		return p.evaluateJavaVersion(ver)
	default:
		return VersionSupported, "railpack", ""
	}
}

func (p *LegacyRuntimePolicy) evaluatePythonVersion(ver string) (VersionStatus, string, string) {
	maj, min, ok := parseMajorMinor(ver)
	if !ok {
		// Single major version or invalid format
		if strings.HasPrefix(ver, "2") {
			return VersionUnsupported, "",
				"Python 2.x is completely end-of-life and unsupported. Security policy prohibits running Python 2.x."
		}
		return VersionSupported, "railpack", ""
	}

	// Python 2 is unsupported
	if maj < 3 {
		return VersionUnsupported, "",
			fmt.Sprintf("Python %s is end-of-life and unsupported. Security policy prohibits running Python 2.x.", ver)
	}

	// Python 3.10 and later is natively supported by Railpack
	if min >= 10 {
		return VersionSupported, "railpack", ""
	}

	// Python 3.5 through 3.9 are legacy versions
	p.mu.RLock()
	allow := p.allowLegacy
	key := fmt.Sprintf("python:%d.%d", maj, min)
	approvedImg, found := p.approvedImages[key]
	p.mu.RUnlock()

	if min >= 5 && min <= 9 {
		if !allow {
			return VersionUnsupported, "",
				fmt.Sprintf("Python %s is an end-of-life legacy version. Legacy container execution is disabled by security policy.", ver)
		}
		if found {
			return VersionProvisioningRequired, "dockerfile", approvedImg
		}
		return VersionUnsupported, "",
			fmt.Sprintf("Python %s has no approved legacy container image. Supported versions are Python >= 3.10 (Railpack) or Python 3.5-3.9 (Approved Legacy Container).", ver)
	}

	// Python 3.0 through 3.4 (no approved images available)
	return VersionUnsupported, "",
		fmt.Sprintf("Python %d.%d is an obsolete legacy version with no approved secure container runtime. Supported versions are Python >= 3.10 (Railpack) or Python 3.5-3.9 (Legacy Dockerfile).", maj, min)
}

func (p *LegacyRuntimePolicy) evaluateNodeVersion(ver string) (VersionStatus, string, string) {
	clean := strings.TrimPrefix(ver, "v")
	parts := strings.Split(clean, ".")
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return VersionSupported, "railpack", ""
	}

	// Node 18, 20, 22, 23 are natively supported
	if maj >= 18 {
		return VersionSupported, "railpack", ""
	}

	// Check approved legacy images for Node 14, 16
	p.mu.RLock()
	allow := p.allowLegacy
	key := fmt.Sprintf("nodejs:%d", maj)
	approvedImg, found := p.approvedImages[key]
	p.mu.RUnlock()

	if maj == 14 || maj == 16 {
		if !allow {
			return VersionUnsupported, "",
				fmt.Sprintf("Node.js %d is an end-of-life legacy version. Legacy container execution is disabled by security policy.", maj)
		}
		if found {
			return VersionProvisioningRequired, "dockerfile", approvedImg
		}
	}

	return VersionUnsupported, "",
		fmt.Sprintf("Node.js %d is obsolete and unsupported. Supported versions are Node.js >= 18 (Railpack) or Node.js 14-16 (Legacy Dockerfile).", maj)
}

func (p *LegacyRuntimePolicy) evaluateGoVersion(ver string) (VersionStatus, string, string) {
	maj, min, ok := parseMajorMinor(ver)
	if !ok {
		return VersionSupported, "railpack", ""
	}

	// Go >= 1.21 supported
	if maj == 1 && min < 20 {
		return VersionUnsupported, "",
			fmt.Sprintf("Go 1.%d is end-of-life. Railpack requires Go 1.21 or later.", min)
	}
	return VersionSupported, "railpack", ""
}

func (p *LegacyRuntimePolicy) evaluateJavaVersion(ver string) (VersionStatus, string, string) {
	clean := strings.TrimPrefix(ver, "v")
	parts := strings.Split(clean, ".")
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return VersionSupported, "railpack", ""
	}

	// Java 17, 21, 23 supported
	if maj >= 17 {
		return VersionSupported, "railpack", ""
	}

	if maj == 11 {
		p.mu.RLock()
		allow := p.allowLegacy
		approvedImg, found := p.approvedImages["java:11"]
		p.mu.RUnlock()
		if !allow {
			return VersionUnsupported, "", "Java 11 legacy runtime execution is disabled by security policy."
		}
		if found {
			return VersionProvisioningRequired, "dockerfile", approvedImg
		}
	}

	return VersionUnsupported, "",
		fmt.Sprintf("Java %d is end-of-life. Supported versions are Java 17, 21, or 23 LTS (Railpack) or Java 11 (Legacy Dockerfile).", maj)
}
