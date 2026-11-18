package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// VersionStatus classifies the support and availability of a requested runtime version.
type VersionStatus string

const (
	VersionSupported            VersionStatus = "VERSION_SUPPORTED"
	VersionUnavailable          VersionStatus = "VERSION_UNAVAILABLE"
	VersionUnsupported          VersionStatus = "VERSION_UNSUPPORTED"
	VersionIncompatible         VersionStatus = "VERSION_INCOMPATIBLE"
	VersionProvisioningRequired VersionStatus = "VERSION_PROVISIONING_REQUIRED"
)

// Supported architectures and operating systems.
var (
	supportedArchitectures = map[string]bool{
		"amd64":   true,
		"x86_64":  true,
		"arm64":   true,
		"aarch64": true,
	}

	supportedOperatingSystems = map[string]bool{
		"linux":  true,
		"darwin": true,
	}
)

// ValidateArchOS verifies whether the given operating system and architecture are supported.
func ValidateArchOS(osName, archName string) (bool, string) {
	cleanOS := strings.ToLower(strings.TrimSpace(osName))
	cleanArch := strings.ToLower(strings.TrimSpace(archName))

	if cleanOS == "" {
		cleanOS = "linux"
	}
	if cleanArch == "" {
		cleanArch = "amd64"
	}

	if !supportedOperatingSystems[cleanOS] {
		return false, fmt.Sprintf("operating system %q is not supported (supported: linux, darwin)", osName)
	}
	if !supportedArchitectures[cleanArch] {
		return false, fmt.Sprintf("CPU architecture %q is not supported (supported: amd64, arm64)", archName)
	}
	return true, ""
}

// NormalizeArch canonicalizes architecture names (e.g. x86_64 -> amd64, aarch64 -> arm64).
func NormalizeArch(arch string) string {
	clean := strings.ToLower(strings.TrimSpace(arch))
	switch clean {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		if clean == "" {
			return "amd64"
		}
		return clean
	}
}

// RuntimeCacheKey uniquely identifies an immutable runtime artifact across sandboxes.
// The key identity strictly accounts for language, exact version, OS, architecture,
// runtime distribution, and builder version.
type RuntimeCacheKey struct {
	Language       string `json:"language"`
	Version        string `json:"version"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	DistributionID string `json:"distribution_id"`
	BuilderVersion string `json:"builder_version"`
}

// DefaultRuntimeCacheKey constructs a key using the host OS and architecture.
func DefaultRuntimeCacheKey(language, version, distID, builderVersion string) RuntimeCacheKey {
	osName := strings.ToLower(runtime.GOOS)
	archName := NormalizeArch(runtime.GOARCH)

	if distID == "" {
		distID = "mise-railpack-2026.10"
	}
	if builderVersion == "" {
		builderVersion = "railpack-0.2.0"
	}

	return RuntimeCacheKey{
		Language:       strings.ToLower(strings.TrimSpace(language)),
		Version:        strings.TrimSpace(version),
		OS:             osName,
		Architecture:   archName,
		DistributionID: strings.TrimSpace(distID),
		BuilderVersion: strings.TrimSpace(builderVersion),
	}
}

// Key computes the deterministic SHA-256 hash identifying this runtime artifact.
func (k RuntimeCacheKey) Key() string {
	lang := strings.ToLower(strings.TrimSpace(k.Language))
	ver := strings.TrimSpace(k.Version)
	osName := strings.ToLower(strings.TrimSpace(k.OS))
	archName := NormalizeArch(k.Architecture)

	raw := fmt.Sprintf("%s:%s:%s:%s:%s:%s",
		lang, ver, osName, archName, strings.TrimSpace(k.DistributionID), strings.TrimSpace(k.BuilderVersion))
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("asl-runtime-%s-%s-%s-%s", lang, ver, archName, hex.EncodeToString(h[:4]))
}

// String provides a human-readable description of the runtime cache identity.
func (k RuntimeCacheKey) String() string {
	return fmt.Sprintf("%s %s (%s/%s, dist=%s, builder=%s)",
		k.Language, k.Version, k.OS, k.Architecture, k.DistributionID, k.BuilderVersion)
}

// RuntimeArtifact represents an immutable, cached runtime distribution or image reference.
type RuntimeArtifact struct {
	Key          RuntimeCacheKey `json:"key"`
	ArtifactRef  string          `json:"artifact_ref"` // e.g. "ghcr.io/railwayapp/railpack-runtime:mise-2026.10.14" or local image
	SizeBytes    int64           `json:"size_bytes"`
	CreatedAt    time.Time       `json:"created_at"`
	LastUsedAt   time.Time       `json:"last_used_at"`
	ActiveBuilds int             `json:"active_builds"` // Number of concurrent builds actively using this artifact
	IsLegacy     bool            `json:"is_legacy"`
	Status       VersionStatus   `json:"status"`
	Builder      string          `json:"builder"`
}

// PackageCacheOptions configures package manager cache isolation and scope.
type PackageCacheOptions struct {
	Language string `json:"language"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	TenantID string `json:"tenant_id,omitempty"`
	Isolated bool   `json:"isolated,omitempty"`
}

// CacheMountConfig specifies BuildKit cache mount parameters for package managers.
type CacheMountConfig struct {
	Type        string `json:"type"`              // "cache"
	Target      string `json:"target"`            // e.g. "/root/.cache/pip"
	ID          string `json:"id"`                // e.g. "asl-pip-linux-amd64"
	SharingMode string `json:"sharing_mode"`      // "shared", "locked", "private"
	TenantID    string `json:"tenant_id,omitempty"`
	ReadOnly    bool   `json:"readonly,omitempty"`
}

// ToBuildKitMountString serializes the cache mount configuration into a Dockerfile syntax mount flag.
func (c CacheMountConfig) ToBuildKitMountString() string {
	sharing := c.SharingMode
	if sharing == "" {
		sharing = "shared"
	}
	res := fmt.Sprintf("--mount=type=cache,id=%s,target=%s,sharing=%s", c.ID, c.Target, sharing)
	if c.ReadOnly {
		res += ",ro"
	}
	return res
}
