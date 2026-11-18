package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SharedRuntimeCache manages persistent BuildKit runtime and package cache identities,
// tracking immutable artifacts, active build leases, LRU eviction, and disk limits.
type SharedRuntimeCache struct {
	mu           sync.RWMutex
	maxSizeBytes int64
	currentBytes int64
	hits         int64
	misses       int64
	artifacts    map[string]*RuntimeArtifact // key.Key() -> artifact
}

// NewSharedRuntimeCache constructs a thread-safe SharedRuntimeCache with a given byte limit.
// A maxSizeBytes <= 0 defaults to 10GB (10 * 1024 * 1024 * 1024).
func NewSharedRuntimeCache(maxSizeBytes int64) *SharedRuntimeCache {
	if maxSizeBytes <= 0 {
		maxSizeBytes = 10 * 1024 * 1024 * 1024 // 10 GB default
	}
	return &SharedRuntimeCache{
		maxSizeBytes: maxSizeBytes,
		artifacts:    make(map[string]*RuntimeArtifact),
	}
}

// Get returns the cached RuntimeArtifact if present, updating its LastUsedAt timestamp.
// If found, returns (artifact, true). Otherwise returns (nil, false).
func (c *SharedRuntimeCache) Get(key RuntimeCacheKey) (*RuntimeArtifact, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.Key()
	artifact, ok := c.artifacts[k]
	if !ok {
		atomic.AddInt64(&c.misses, 1)
		return nil, false
	}

	atomic.AddInt64(&c.hits, 1)
	artifact.LastUsedAt = time.Now().UTC()
	// Return a copy to prevent mutation outside lock
	cp := *artifact
	return &cp, true
}

// Put stores or updates a runtime artifact in the cache, enforcing eviction if needed.
// Active builds must be preserved. If size requires eviction, oldest unused artifacts are purged.
func (c *SharedRuntimeCache) Put(artifact *RuntimeArtifact) error {
	if artifact == nil {
		return fmt.Errorf("runtime cache: artifact cannot be nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	k := artifact.Key.Key()
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}
	artifact.LastUsedAt = now

	// If already present, subtract old size
	if existing, found := c.artifacts[k]; found {
		c.currentBytes -= existing.SizeBytes
		// Preserve active build count if previously leased
		if artifact.ActiveBuilds == 0 && existing.ActiveBuilds > 0 {
			artifact.ActiveBuilds = existing.ActiveBuilds
		}
	}

	// Evict to make room for new artifact if necessary
	if c.currentBytes+artifact.SizeBytes > c.maxSizeBytes {
		c.evictToFitLocked(artifact.SizeBytes)
	}

	// Store copy
	cp := *artifact
	c.artifacts[k] = &cp
	c.currentBytes += artifact.SizeBytes
	return nil
}

// AcquireLease registers an active build using the given key, preventing eviction.
func (c *SharedRuntimeCache) AcquireLease(key RuntimeCacheKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.Key()
	art, ok := c.artifacts[k]
	if !ok {
		return false
	}
	art.ActiveBuilds++
	art.LastUsedAt = time.Now().UTC()
	return true
}

// ReleaseLease decrements active build count on the artifact.
func (c *SharedRuntimeCache) ReleaseLease(key RuntimeCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.Key()
	art, ok := c.artifacts[k]
	if ok && art.ActiveBuilds > 0 {
		art.ActiveBuilds--
	}
}

// HasActiveLease returns true if the artifact exists and is currently in use by an active build.
func (c *SharedRuntimeCache) HasActiveLease(key RuntimeCacheKey) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	k := key.Key()
	art, ok := c.artifacts[k]
	return ok && art.ActiveBuilds > 0
}

// Evict explicitly deletes an artifact if it is not currently leased by active builds.
// Returns true if evicted, or false if not found or active.
func (c *SharedRuntimeCache) Evict(key RuntimeCacheKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.Key()
	art, ok := c.artifacts[k]
	if !ok {
		return false
	}
	// Never delete an artifact actively leased by running builds
	if art.ActiveBuilds > 0 {
		return false
	}

	delete(c.artifacts, k)
	c.currentBytes -= art.SizeBytes
	return true
}

// EvictToFit triggers LRU eviction to free up requiredBytes. Active builds are strictly preserved.
func (c *SharedRuntimeCache) EvictToFit(requiredBytes int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evictToFitLocked(requiredBytes)
}

// evictToFitLocked evicts least-recently-used artifacts whose ActiveBuilds == 0.
func (c *SharedRuntimeCache) evictToFitLocked(requiredBytes int64) int64 {
	type candidate struct {
		key        string
		size       int64
		lastUsedAt time.Time
	}

	var candidates []candidate
	for k, art := range c.artifacts {
		// Only consider artifacts without active builds
		if art.ActiveBuilds == 0 {
			candidates = append(candidates, candidate{
				key:        k,
				size:       art.SizeBytes,
				lastUsedAt: art.LastUsedAt,
			})
		}
	}

	// Sort oldest first (LRU)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastUsedAt.Before(candidates[j].lastUsedAt)
	})

	var freedBytes int64
	for _, cand := range candidates {
		if c.currentBytes+requiredBytes <= c.maxSizeBytes {
			break
		}
		delete(c.artifacts, cand.key)
		c.currentBytes -= cand.size
		freedBytes += cand.size
	}
	return freedBytes
}

// Stats returns the current count, total size in bytes, capacity limit, and hit/miss counts.
func (c *SharedRuntimeCache) Stats() (count int, totalBytes, maxBytes, hits, misses int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.artifacts), c.currentBytes, c.maxSizeBytes, atomic.LoadInt64(&c.hits), atomic.LoadInt64(&c.misses)
}

// GetAll returns a copy of all cached runtime artifacts.
func (c *SharedRuntimeCache) GetAll() []*RuntimeArtifact {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res := make([]*RuntimeArtifact, 0, len(c.artifacts))
	for _, art := range c.artifacts {
		cp := *art
		res = append(res, &cp)
	}
	return res
}

// Clear removes all inactive runtime artifacts, preserving any with active leases.
// Returns the number of evicted artifacts.
func (c *SharedRuntimeCache) Clear() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	var evicted int
	for k, art := range c.artifacts {
		if art.ActiveBuilds == 0 {
			delete(c.artifacts, k)
			c.currentBytes -= art.SizeBytes
			evicted++
		}
	}
	return evicted
}

// GetPackageCacheMounts returns standard BuildKit cache mount configurations for package managers
// based on language, operating system, and architecture.
func GetPackageCacheMounts(language, osName, archName string) []CacheMountConfig {
	return GetPackageCacheMountsScoped(PackageCacheOptions{
		Language: language,
		OS:       osName,
		Arch:     archName,
	})
}

// GetPackageCacheMountsScoped generates BuildKit cache mount configurations with tenant isolation options.
func GetPackageCacheMountsScoped(opts PackageCacheOptions) []CacheMountConfig {
	lang := strings.ToLower(strings.TrimSpace(opts.Language))
	osName := strings.ToLower(strings.TrimSpace(opts.OS))
	if osName == "" {
		osName = "linux"
	}
	archName := NormalizeArch(opts.Arch)

	tenantPrefix := ""
	if opts.Isolated && opts.TenantID != "" {
		tenantPrefix = fmt.Sprintf("tenant-%s-", strings.ToLower(strings.TrimSpace(opts.TenantID)))
	}

	switch lang {
	case "python":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/root/.cache/pip",
				ID:          fmt.Sprintf("asl-%spip-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/root/.cache/uv",
				ID:          fmt.Sprintf("asl-%suv-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
		}
	case "node", "nodejs", "javascript", "typescript":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/root/.npm",
				ID:          fmt.Sprintf("asl-%snpm-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/root/.cache/yarn",
				ID:          fmt.Sprintf("asl-%syarn-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/root/.local/share/pnpm/store",
				ID:          fmt.Sprintf("asl-%spnpm-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
		}
	case "go", "golang":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/go/pkg/mod",
				ID:          fmt.Sprintf("asl-%sgomod-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/root/.cache/go-build",
				ID:          fmt.Sprintf("asl-%sgobuild-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "locked", // Locking semantics for compiler build cache
				TenantID:    opts.TenantID,
			},
		}
	case "java":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/root/.m2/repository",
				ID:          fmt.Sprintf("asl-%sm2-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/root/.gradle/caches",
				ID:          fmt.Sprintf("asl-%sgradle-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "locked", // Locking semantics to prevent concurrent Gradle cache locks
				TenantID:    opts.TenantID,
			},
		}
	case "rust":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/usr/local/cargo/registry",
				ID:          fmt.Sprintf("asl-%scargo-reg-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
			{
				Type:        "cache",
				Target:      "/usr/local/cargo/git",
				ID:          fmt.Sprintf("asl-%scargo-git-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "shared",
				TenantID:    opts.TenantID,
			},
		}
	case "c", "cpp", "c++":
		return []CacheMountConfig{
			{
				Type:        "cache",
				Target:      "/root/.cache/ccache",
				ID:          fmt.Sprintf("asl-%sccache-%s-%s", tenantPrefix, osName, archName),
				SharingMode: "locked",
				TenantID:    opts.TenantID,
			},
		}
	default:
		return nil
	}
}

// BuildKitCacheMountArgs converts a slice of CacheMountConfig into command line flags or Dockerfile syntax strings.
func BuildKitCacheMountArgs(mounts []CacheMountConfig) []string {
	res := make([]string, 0, len(mounts))
	for _, m := range mounts {
		res = append(res, m.ToBuildKitMountString())
	}
	return res
}
