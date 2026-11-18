package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIntegration_RuntimeProvisioningAndReuse verifies:
// 1. First Python 3.11 build provisions or obtains the runtime.
// 2. Second independent sandbox reuses the runtime (cache hit).
// 3. Rebuilding after source changes does not unnecessarily download Python again.
func TestIntegration_RuntimeProvisioningAndReuse(t *testing.T) {
	cache := NewSharedRuntimeCache(500 * 1024 * 1024)
	policy := NewLegacyRuntimePolicy()
	mgr := NewRuntimeAvailabilityManager(cache, policy, nil)

	var downloadAndProvisionOps int64
	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		atomic.AddInt64(&downloadAndProvisionOps, 1)
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: fmt.Sprintf("ghcr.io/railwayapp/railpack-runtime:%s-%s", key.Language, key.Version),
			SizeBytes:   45 * 1024 * 1024,
			IsLegacy:    false,
			Builder:     "railpack",
			CreatedAt:   time.Now().UTC(),
		}, nil
	})

	keyPython311 := DefaultRuntimeCacheKey("python", "3.11.9", "", "")

	// --- Step 1: Sandbox 1 initial build (Cache Miss & Provisioning) ---
	status1, art1Check, err := mgr.CheckAvailability(keyPython311)
	if err != nil {
		t.Fatalf("sandbox 1 availability check failed: %v", err)
	}
	if status1 != VersionProvisioningRequired || art1Check != nil {
		t.Errorf("expected VersionProvisioningRequired on fresh cache; got status=%s, art=%v", status1, art1Check)
	}

	art1, status1Acq, err := mgr.CheckAndAcquireRuntime(context.Background(), keyPython311)
	if err != nil {
		t.Fatalf("sandbox 1 acquire failed: %v", err)
	}
	if status1Acq != VersionSupported || art1 == nil {
		t.Fatalf("sandbox 1 expected VersionSupported, got %s", status1Acq)
	}
	if atomic.LoadInt64(&downloadAndProvisionOps) != 1 {
		t.Errorf("expected exactly 1 provision operation for sandbox 1; got %d", downloadAndProvisionOps)
	}

	// Verify build lease was acquired during active build
	if !cache.HasActiveLease(keyPython311) {
		t.Errorf("expected active lease for sandbox 1")
	}

	// Sandbox 1 build finishes -> release lease
	mgr.ReleaseRuntime(keyPython311)
	if cache.HasActiveLease(keyPython311) {
		t.Errorf("expected lease cleared after sandbox 1 build completion")
	}

	// --- Step 2: Sandbox 2 independent build (Cache Hit & Reuse) ---
	status2, art2Check, err := mgr.CheckAvailability(keyPython311)
	if err != nil {
		t.Fatalf("sandbox 2 check failed: %v", err)
	}
	if status2 != VersionSupported || art2Check == nil {
		t.Fatalf("expected sandbox 2 cache hit (VersionSupported); got %s", status2)
	}

	art2, status2Acq, err := mgr.CheckAndAcquireRuntime(context.Background(), keyPython311)
	if err != nil {
		t.Fatalf("sandbox 2 acquire failed: %v", err)
	}
	if status2Acq != VersionSupported {
		t.Errorf("sandbox 2 expected VersionSupported, got %s", status2Acq)
	}
	if art2.ArtifactRef != art1.ArtifactRef {
		t.Errorf("expected reused artifact reference: got %s, want %s", art2.ArtifactRef, art1.ArtifactRef)
	}

	// Invariant: Provisioning counter must STILL be 1! No re-download occurred!
	if ops := atomic.LoadInt64(&downloadAndProvisionOps); ops != 1 {
		t.Errorf("CRITICAL: repeated download occurred for sandbox 2! Expected 1 total op, got %d", ops)
	}

	mgr.ReleaseRuntime(keyPython311)

	// --- Step 3: Rebuilding sandbox 2 after source code changes ---
	// Source changes (simulated app edits)
	statusRebuild, artRebuild, err := mgr.CheckAvailability(keyPython311)
	if err != nil || statusRebuild != VersionSupported || artRebuild == nil {
		t.Fatalf("rebuild availability check failed: status=%s, err=%v", statusRebuild, err)
	}

	artRebuildAcq, _, err := mgr.CheckAndAcquireRuntime(context.Background(), keyPython311)
	if err != nil {
		t.Fatalf("rebuild acquire failed: %v", err)
	}
	if artRebuildAcq.ArtifactRef != art1.ArtifactRef {
		t.Errorf("rebuild must reuse existing runtime artifact")
	}

	// Still exactly 1 provision op across all builds!
	if ops := atomic.LoadInt64(&downloadAndProvisionOps); ops != 1 {
		t.Errorf("CRITICAL: runtime was re-downloaded after source changes! Total ops=%d", ops)
	}

	mgr.ReleaseRuntime(keyPython311)

	// Verify hit/miss statistics
	_, _, _, hits, misses := cache.Stats()
	if hits < 2 || misses < 1 {
		t.Errorf("expected at least 2 hits and 1 miss; got hits=%d, misses=%d", hits, misses)
	}
}

// TestIntegration_DistinctVersionsAndArchitectures verifies:
// 1. Different Python versions use distinct runtime artifacts.
// 2. Different architectures do not incorrectly share incompatible artifacts.
func TestIntegration_DistinctVersionsAndArchitectures(t *testing.T) {
	cache := NewSharedRuntimeCache(100 * 1024 * 1024)
	mgr := NewRuntimeAvailabilityManager(cache, nil, nil)

	provisioned := make(map[string]string)
	var mu sync.Mutex

	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		mu.Lock()
		defer mu.Unlock()
		ref := fmt.Sprintf("ghcr.io/railwayapp/railpack-runtime:%s-%s-%s-%s",
			key.Language, key.Version, key.OS, key.Architecture)
		provisioned[key.Key()] = ref
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: ref,
			SizeBytes:   20 * 1024 * 1024,
		}, nil
	})

	// Version 1: Python 3.11.9 on amd64
	k311Amd64 := RuntimeCacheKey{
		Language:       "python",
		Version:        "3.11.9",
		OS:             "linux",
		Architecture:   "amd64",
		DistributionID: "mise-railpack-2026.10",
		BuilderVersion: "railpack-0.2.0",
	}

	// Version 2: Python 3.12.7 on amd64
	k312Amd64 := RuntimeCacheKey{
		Language:       "python",
		Version:        "3.12.7",
		OS:             "linux",
		Architecture:   "amd64",
		DistributionID: "mise-railpack-2026.10",
		BuilderVersion: "railpack-0.2.0",
	}

	// Architecture 2: Python 3.11.9 on arm64
	k311Arm64 := RuntimeCacheKey{
		Language:       "python",
		Version:        "3.11.9",
		OS:             "linux",
		Architecture:   "arm64",
		DistributionID: "mise-railpack-2026.10",
		BuilderVersion: "railpack-0.2.0",
	}

	art311Amd64, _, _ := mgr.CheckAndAcquireRuntime(context.Background(), k311Amd64)
	art312Amd64, _, _ := mgr.CheckAndAcquireRuntime(context.Background(), k312Amd64)
	art311Arm64, _, _ := mgr.CheckAndAcquireRuntime(context.Background(), k311Arm64)

	// Distinct versions must have distinct artifacts
	if art311Amd64.ArtifactRef == art312Amd64.ArtifactRef {
		t.Errorf("Python 3.11 and 3.12 incorrectly shared artifact: %s", art311Amd64.ArtifactRef)
	}

	// Distinct architectures must NEVER share artifacts
	if art311Amd64.ArtifactRef == art311Arm64.ArtifactRef {
		t.Errorf("amd64 and arm64 incorrectly shared artifact: %s", art311Amd64.ArtifactRef)
	}

	// Cache must hold 3 distinct artifacts
	count, _, _, _, _ := cache.Stats()
	if count != 3 {
		t.Errorf("expected 3 distinct artifacts in cache; got %d", count)
	}
}

// TestIntegration_BuildKitPersistentCacheBehavior verifies:
// 1. BuildKit cache mounts emit standard syntax.
// 2. Build logs confirm persistent BuildKit cache hits.
func TestIntegration_BuildKitPersistentCacheBehavior(t *testing.T) {
	// 1. Cache mounts verification
	mounts := GetPackageCacheMounts("python", "linux", "amd64")
	var hasPip, hasUV bool
	for _, m := range mounts {
		if strings.Contains(m.ToBuildKitMountString(), "target=/root/.cache/pip") {
			hasPip = true
		}
		if strings.Contains(m.ToBuildKitMountString(), "target=/root/.cache/uv") {
			hasUV = true
		}
	}
	if !hasPip || !hasUV {
		t.Errorf("expected pip and uv cache mounts, got: %+v", mounts)
	}

	// 2. Affirmative BuildKit cache detection
	sampleBuildKitLogHit := `
#1 [internal] load build definition from Dockerfile
#1 transferring dockerfile: 32B done
#2 [internal] load .dockerignore
#3 [1/4] FROM docker.io/library/python:3.11-slim
#3 CACHED
#4 [2/4] WORKDIR /app
#4 CACHED
#5 [3/4] RUN --mount=type=cache,id=asl-pip-linux-amd64,target=/root/.cache/pip pip install -r requirements.txt
#5 CACHED
#6 exporting to image
#6 exporting layers done
`
	hitRes := detectCacheHit("railpack", sampleBuildKitLogHit)
	if hitRes == nil || !*hitRes {
		t.Errorf("expected affirmative cache hit detection from BuildKit log with CACHED markers")
	}

	sampleBuildKitLogMiss := `
#1 [internal] load build definition from Dockerfile
=> [1/4] FROM docker.io/library/python:3.11-slim
=> [2/4] RUN pip install -r requirements.txt
1.234 Downloading numpy-2.0.0.tar.gz
15.67 Successfully installed numpy-2.0.0
=> DONE
`
	missRes := detectCacheHit("railpack", sampleBuildKitLogMiss)
	if missRes == nil || *missRes {
		t.Errorf("expected affirmative cache miss detection from BuildKit log without CACHED markers")
	}
}

// TestIntegration_LegacyPython35Staging verifies:
// 1. Request for Python 3.5 creates an isolated legacy strategy plan.
// 2. Context staging generates isolated legacy Dockerfile with approved image python:3.5-slim.
// 3. Original user repository is completely untouched.
func TestIntegration_LegacyPython35Staging(t *testing.T) {
	tmpRepo := t.TempDir()
	appFile := filepath.Join(tmpRepo, "app.py")
	_ = os.WriteFile(appFile, []byte("print('legacy python 3.5 application')\n"), 0o644)
	reqFile := filepath.Join(tmpRepo, "requirements.txt")
	_ = os.WriteFile(reqFile, []byte("requests==2.18.4\n"), 0o644)

	planner := NewBuildPlanner()
	userCfg := &UserBuildConfig{
		Language:       "python",
		RuntimeVersion: "3.5",
	}

	plan, err := planner.PlanBuild(context.Background(), "sbx-leg-1", tmpRepo, userCfg)
	if err != nil {
		t.Fatalf("PlanBuild failed: %v", err)
	}

	// Verify plan attributes
	if plan.SelectedBuilder != "dockerfile" || plan.BuilderSelectionSource != "LEGACY_RUNTIME_STRATEGY" {
		t.Errorf("expected isolated legacy dockerfile builder, got builder=%s source=%s",
			plan.SelectedBuilder, plan.BuilderSelectionSource)
	}
	if plan.Runtime.Version != "3.5" {
		t.Errorf("expected version 3.5 preserved, got %s", plan.Runtime.Version)
	}
	if plan.Runtime.LegacyImage != "python:3.5-slim" {
		t.Errorf("expected legacy image python:3.5-slim, got %s", plan.Runtime.LegacyImage)
	}

	// Verify context staging
	stagingEngine := NewContextStagingEngine()
	policy := DefaultContextPolicy()
	staged, err := stagingEngine.Stage(context.Background(), plan, "sbx-leg-1", "dep-1", tmpRepo, policy)
	if err != nil {
		t.Fatalf("staging failed: %v", err)
	}
	defer staged.Cleanup()

	// Invariant: Original repo must NOT contain a Dockerfile
	if _, statErr := os.Stat(filepath.Join(tmpRepo, "Dockerfile")); !os.IsNotExist(statErr) {
		t.Errorf("CRITICAL: Dockerfile was written to original repository!")
	}

	// Staging dir MUST contain injected legacy Dockerfile
	stagedDF := filepath.Join(staged.StagingDir, "Dockerfile")
	dfData, err := os.ReadFile(stagedDF)
	if err != nil {
		t.Fatalf("staged Dockerfile missing: %v", err)
	}
	dfContent := string(dfData)
	if !strings.Contains(dfContent, "FROM python:3.5-slim") {
		t.Errorf("staged Dockerfile missing approved image: %s", dfContent)
	}
	if !strings.Contains(dfContent, "--mount=type=cache") || !strings.Contains(dfContent, "target=/root/.cache/pip") {
		t.Errorf("staged Dockerfile missing BuildKit pip cache mount: %s", dfContent)
	}
}

// TestIntegration_ConcurrentRequestsNoCorruption verifies:
// Concurrent requests do not cause duplicate provisioning or cache corruption under heavy load.
func TestIntegration_ConcurrentRequestsNoCorruption(t *testing.T) {
	cache := NewSharedRuntimeCache(500 * 1024 * 1024)
	mgr := NewRuntimeAvailabilityManager(cache, nil, nil)

	var totalProvisioned int64
	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt64(&totalProvisioned, 1)
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: "artifact-" + key.Version,
			SizeBytes:   10 * 1024 * 1024,
		}, nil
	})

	const goroutines = 30
	var wg sync.WaitGroup

	// 30 goroutines across 3 distinct Python versions (10 each)
	versions := []string{"3.10.14", "3.11.9", "3.12.7"}

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		v := versions[i%len(versions)]
		go func(version string) {
			defer wg.Done()
			k := DefaultRuntimeCacheKey("python", version, "", "")
			art, status, err := mgr.CheckAndAcquireRuntime(context.Background(), k)
			if err != nil || status != VersionSupported || art == nil {
				t.Errorf("failed acquire for %s: %v", version, err)
			}
			time.Sleep(5 * time.Millisecond)
			mgr.ReleaseRuntime(k)
		}(v)
	}

	wg.Wait()

	// Exactly 3 provisioning operations (1 per version)
	provisions := atomic.LoadInt64(&totalProvisioned)
	if provisions != int64(len(versions)) {
		t.Errorf("expected exactly %d provisioning operations (1 per unique version), got %d", len(versions), provisions)
	}

	count, _, _, _, _ := cache.Stats()
	if count != len(versions) {
		t.Errorf("expected %d artifacts in cache, got %d", len(versions), count)
	}
}
