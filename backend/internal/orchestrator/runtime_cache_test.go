package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeCacheKey_IdentityAndDeterminism(t *testing.T) {
	// Account for: Language, Exact Version, OS, Architecture, DistributionID, BuilderVersion
	k1 := DefaultRuntimeCacheKey("Python", "3.11.9", "mise-railpack-2026.10", "railpack-0.2.0")
	k2 := DefaultRuntimeCacheKey("python", "3.11.9", "mise-railpack-2026.10", "railpack-0.2.0")

	// Case normalization
	if k1.Key() != k2.Key() {
		t.Errorf("expected deterministic keys regardless of case; got %s vs %s", k1.Key(), k2.Key())
	}

	// Different exact versions must produce distinct keys
	kVerDiff := DefaultRuntimeCacheKey("python", "3.12.7", "mise-railpack-2026.10", "railpack-0.2.0")
	if k1.Key() == kVerDiff.Key() {
		t.Errorf("different versions must produce distinct cache keys: %s", k1.Key())
	}

	// Different architectures must produce distinct keys
	kArch1 := RuntimeCacheKey{
		Language:       "python",
		Version:        "3.11.9",
		OS:             "linux",
		Architecture:   "amd64",
		DistributionID: "mise-railpack-2026.10",
		BuilderVersion: "railpack-0.2.0",
	}
	kArch2 := RuntimeCacheKey{
		Language:       "python",
		Version:        "3.11.9",
		OS:             "linux",
		Architecture:   "arm64",
		DistributionID: "mise-railpack-2026.10",
		BuilderVersion: "railpack-0.2.0",
	}
	if kArch1.Key() == kArch2.Key() {
		t.Errorf("different architectures must NOT share cache keys: %s vs %s", kArch1.Key(), kArch2.Key())
	}

	// Different builders or distributions must produce distinct keys
	kDistDiff := kArch1
	kDistDiff.DistributionID = "mise-railpack-2027.01"
	if kArch1.Key() == kDistDiff.Key() {
		t.Errorf("different distributions must produce distinct cache keys")
	}

	kBuilderDiff := kArch1
	kBuilderDiff.BuilderVersion = "railpack-0.3.0"
	if kArch1.Key() == kBuilderDiff.Key() {
		t.Errorf("different builder versions must produce distinct cache keys")
	}
}

func TestSharedRuntimeCache_GetPutLease(t *testing.T) {
	cache := NewSharedRuntimeCache(100 * 1024 * 1024) // 100 MB limit

	key := DefaultRuntimeCacheKey("python", "3.11.9", "", "")
	artifact := &RuntimeArtifact{
		Key:         key,
		ArtifactRef: "ghcr.io/railwayapp/railpack-runtime:python-3.11.9",
		SizeBytes:   20 * 1024 * 1024,
	}

	// Initial miss
	if _, found := cache.Get(key); found {
		t.Errorf("expected miss on empty cache")
	}

	// Put
	if err := cache.Put(artifact); err != nil {
		t.Fatalf("put failed: %v", err)
	}

	// Hit
	cached, found := cache.Get(key)
	if !found {
		t.Fatalf("expected hit after put")
	}
	if cached.ArtifactRef != artifact.ArtifactRef {
		t.Errorf("artifact ref mismatch: got %s, want %s", cached.ArtifactRef, artifact.ArtifactRef)
	}

	// Verify hit/miss counts
	count, totalBytes, maxBytes, hits, misses := cache.Stats()
	if count != 1 || totalBytes != 20*1024*1024 || maxBytes != 100*1024*1024 || hits != 1 || misses != 1 {
		t.Errorf("unexpected stats: count=%d, total=%d, hits=%d, misses=%d", count, totalBytes, hits, misses)
	}

	// Acquire lease
	if !cache.AcquireLease(key) {
		t.Fatalf("failed to acquire lease")
	}
	if !cache.HasActiveLease(key) {
		t.Errorf("expected active lease")
	}

	// Release lease
	cache.ReleaseLease(key)
	if cache.HasActiveLease(key) {
		t.Errorf("expected no active lease after release")
	}
}

func TestSharedRuntimeCache_EvictionRespectsActiveLeases(t *testing.T) {
	// Small cache capacity: 50MB
	cache := NewSharedRuntimeCache(50 * 1024 * 1024)

	k1 := DefaultRuntimeCacheKey("python", "3.10.14", "", "")
	art1 := &RuntimeArtifact{
		Key:         k1,
		ArtifactRef: "python-3.10",
		SizeBytes:   25 * 1024 * 1024,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		LastUsedAt:  time.Now().Add(-2 * time.Hour),
	}

	k2 := DefaultRuntimeCacheKey("python", "3.11.9", "", "")
	art2 := &RuntimeArtifact{
		Key:         k2,
		ArtifactRef: "python-3.11",
		SizeBytes:   25 * 1024 * 1024,
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		LastUsedAt:  time.Now().Add(-1 * time.Hour),
	}

	_ = cache.Put(art1)
	_ = cache.Put(art2)

	// Lease art1 (active build)
	cache.AcquireLease(k1)
	if !cache.HasActiveLease(k1) {
		t.Fatalf("art1 must have active lease")
	}

	// Now add art3 (25MB), exceeding 50MB limit
	k3 := DefaultRuntimeCacheKey("python", "3.12.7", "", "")
	art3 := &RuntimeArtifact{
		Key:         k3,
		ArtifactRef: "python-3.12",
		SizeBytes:   25 * 1024 * 1024,
	}

	if err := cache.Put(art3); err != nil {
		t.Fatalf("put art3 failed: %v", err)
	}

	// art1 must NOT be evicted because ActiveBuilds > 0, even though it's older than art2!
	if _, found := cache.Get(k1); !found {
		t.Errorf("CRITICAL BUG: actively leased artifact art1 was evicted!")
	}

	// art2 had ActiveBuilds == 0, so art2 was evicted to make room
	if _, found := cache.Get(k2); found {
		t.Errorf("expected inactive artifact art2 to be evicted")
	}

	// art3 must be present
	if _, found := cache.Get(k3); !found {
		t.Errorf("expected new artifact art3 to be present")
	}

	// Release lease on art1 and verify it can be evicted manually
	cache.ReleaseLease(k1)
	if !cache.Evict(k1) {
		t.Errorf("expected successful eviction of art1 after lease release")
	}
}

func TestPackageManagerCaches_IsolationAndMounts(t *testing.T) {
	// Standard shared package cache
	mountsPython := GetPackageCacheMounts("python", "linux", "amd64")
	if len(mountsPython) != 2 {
		t.Fatalf("expected 2 mounts for python (pip and uv); got %d", len(mountsPython))
	}
	if mountsPython[0].Target != "/root/.cache/pip" || mountsPython[1].Target != "/root/.cache/uv" {
		t.Errorf("unexpected python targets: %+v", mountsPython)
	}

	mountStr := mountsPython[0].ToBuildKitMountString()
	if !strings.Contains(mountStr, "--mount=type=cache") || !strings.Contains(mountStr, "target=/root/.cache/pip") {
		t.Errorf("invalid mount string: %s", mountStr)
	}

	// Tenant isolated cache mount
	opts := PackageCacheOptions{
		Language: "python",
		OS:       "linux",
		Arch:     "amd64",
		TenantID: "tenant-abc",
		Isolated: true,
	}
	isolatedMounts := GetPackageCacheMountsScoped(opts)
	if len(isolatedMounts) == 0 {
		t.Fatalf("expected isolated mounts")
	}
	if !strings.Contains(isolatedMounts[0].ID, "tenant-abc") {
		t.Errorf("isolated mount ID must include tenant ID: got %s", isolatedMounts[0].ID)
	}

	// Verify locking semantics for compilers (Go build cache, Gradle, ccache)
	goMounts := GetPackageCacheMounts("go", "linux", "amd64")
	var goBuildMount *CacheMountConfig
	for i := range goMounts {
		if goMounts[i].Target == "/root/.cache/go-build" {
			goBuildMount = &goMounts[i]
		}
	}
	if goBuildMount == nil || goBuildMount.SharingMode != "locked" {
		t.Errorf("Go build cache must have locked sharing mode to avoid concurrent write corruption")
	}

	javaMounts := GetPackageCacheMounts("java", "linux", "amd64")
	var gradleMount *CacheMountConfig
	for i := range javaMounts {
		if javaMounts[i].Target == "/root/.gradle/caches" {
			gradleMount = &javaMounts[i]
		}
	}
	if gradleMount == nil || gradleMount.SharingMode != "locked" {
		t.Errorf("Gradle cache must have locked sharing mode")
	}
}

func TestSharedRuntimeCache_ConcurrentAccess(t *testing.T) {
	cache := NewSharedRuntimeCache(500 * 1024 * 1024)
	var wg sync.WaitGroup

	numGoroutines := 30
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ver := fmt.Sprintf("3.11.%d", id%5)
			k := DefaultRuntimeCacheKey("python", ver, "", "")
			art := &RuntimeArtifact{
				Key:         k,
				ArtifactRef: "python-" + ver,
				SizeBytes:   10 * 1024 * 1024,
			}
			_ = cache.Put(art)
			cache.AcquireLease(k)
			time.Sleep(2 * time.Millisecond)
			_, _ = cache.Get(k)
			cache.ReleaseLease(k)
		}(i)
	}

	wg.Wait()
	count, _, _, _, _ := cache.Stats()
	if count == 0 {
		t.Errorf("expected non-zero artifacts after concurrent access")
	}
}
