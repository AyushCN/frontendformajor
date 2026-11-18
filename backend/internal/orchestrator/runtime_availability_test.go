package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeAvailability_SingleflightConcurrency(t *testing.T) {
	cache := NewSharedRuntimeCache(100 * 1024 * 1024)
	policy := NewLegacyRuntimePolicy()
	mgr := NewRuntimeAvailabilityManager(cache, policy, nil)

	var provisionCount int64
	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		// Simulate network download/compilation delay
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt64(&provisionCount, 1)
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: "ghcr.io/railwayapp/railpack-runtime:" + key.Language + "-" + key.Version,
			SizeBytes:   25 * 1024 * 1024,
			IsLegacy:    false,
		}, nil
	})

	key := DefaultRuntimeCacheKey("python", "3.11.9", "", "")

	// 20 concurrent goroutines simultaneously requesting the exact same missing runtime
	const numGoroutines = 20
	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)
	artifacts := make([]*RuntimeArtifact, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			art, status, err := mgr.CheckAndAcquireRuntime(context.Background(), key)
			if err != nil {
				errCh <- err
				return
			}
			if status != VersionSupported {
				errCh <- fmt.Errorf("expected VersionSupported, got %s", status)
				return
			}
			artifacts[idx] = art
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent acquire error: %v", err)
	}

	// Verify that single-flight deduplicated the 20 requests down to EXACTLY 1 provision run!
	actualProvisions := atomic.LoadInt64(&provisionCount)
	if actualProvisions != 1 {
		t.Errorf("CRITICAL CONCURRENCY BUG: single-flight failed! Expected 1 provision call, got %d", actualProvisions)
	}

	// Verify all returned artifacts have the expected reference
	for i, art := range artifacts {
		if art == nil || art.ArtifactRef != "ghcr.io/railwayapp/railpack-runtime:python-3.11.9" {
			t.Errorf("goroutine %d received invalid artifact: %+v", i, art)
		}
	}

	// Verify cache contains the artifact
	if _, found := cache.Get(key); !found {
		t.Errorf("artifact was not cached in SharedRuntimeCache")
	}

	// Verify active builds equals numGoroutines
	cached, _ := cache.Get(key)
	if cached.ActiveBuilds != numGoroutines {
		t.Errorf("expected %d active builds, got %d", numGoroutines, cached.ActiveBuilds)
	}

	// Release all leases
	for i := 0; i < numGoroutines; i++ {
		mgr.ReleaseRuntime(key)
	}
	cachedAfter, _ := cache.Get(key)
	if cachedAfter.ActiveBuilds != 0 {
		t.Errorf("expected 0 active builds after release, got %d", cachedAfter.ActiveBuilds)
	}
}

func TestRuntimeAvailability_CancellationResilience(t *testing.T) {
	cache := NewSharedRuntimeCache(100 * 1024 * 1024)
	mgr := NewRuntimeAvailabilityManager(cache, nil, nil)

	started := make(chan struct{})
	var provisionSuccess int64

	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		close(started)
		// Long running provisioning
		time.Sleep(100 * time.Millisecond)
		atomic.AddInt64(&provisionSuccess, 1)
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: "python-3.11-resilient",
			SizeBytes:   10 * 1024 * 1024,
		}, nil
	})

	key := DefaultRuntimeCacheKey("python", "3.11.9", "", "")

	// Caller 1 has a short timeout that cancels before provisioning finishes
	ctxShort, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()

	// Caller 2 has an independent context with sufficient timeout
	ctxLong, cancelLong := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelLong()

	var wg sync.WaitGroup
	var errCaller1, errCaller2 error
	var artCaller2 *RuntimeArtifact

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, errCaller1 = mgr.CheckAndAcquireRuntime(ctxShort, key)
	}()

	<-started // wait for provisioner to begin

	wg.Add(1)
	go func() {
		defer wg.Done()
		var status VersionStatus
		artCaller2, status, errCaller2 = mgr.CheckAndAcquireRuntime(ctxLong, key)
		if status != VersionSupported {
			errCaller2 = fmt.Errorf("caller2 expected VersionSupported, got %s", status)
		}
	}()

	wg.Wait()

	// Caller 1 was cancelled
	if errCaller1 == nil || !errors.Is(ctxShort.Err(), context.DeadlineExceeded) {
		t.Errorf("expected caller1 to get context deadline exceeded, got: %v", errCaller1)
	}

	// Caller 2 must have succeeded despite caller 1 cancellation!
	if errCaller2 != nil {
		t.Errorf("caller 2 failed due to caller 1 cancellation: %v", errCaller2)
	}
	if artCaller2 == nil || artCaller2.ArtifactRef != "python-3.11-resilient" {
		t.Errorf("caller 2 received invalid artifact: %+v", artCaller2)
	}

	// Cache must not be corrupted
	if art, ok := cache.Get(key); !ok || art.ArtifactRef != "python-3.11-resilient" {
		t.Errorf("shared cache corrupted or missing artifact")
	}
}

func TestRuntimeAvailability_FailureBackoffAndRetry(t *testing.T) {
	cache := NewSharedRuntimeCache(100 * 1024 * 1024)
	mgr := NewRuntimeAvailabilityManager(cache, nil, nil)
	mgr.SetBackoffDuration(50 * time.Millisecond) // short for test

	var attemptCount int64
	shouldFail := true

	mgr.SetProvisionFunc(func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error) {
		atomic.AddInt64(&attemptCount, 1)
		if shouldFail {
			return nil, fmt.Errorf("simulated network outage downloading compiler")
		}
		return &RuntimeArtifact{
			Key:         key,
			ArtifactRef: "recovered-python-3.11",
			SizeBytes:   10 * 1024 * 1024,
		}, nil
	})

	key := DefaultRuntimeCacheKey("python", "3.11.9", "", "")

	// Attempt 1: Fails
	_, status, err := mgr.CheckAndAcquireRuntime(context.Background(), key)
	if err == nil || status != VersionUnavailable {
		t.Fatalf("expected VersionUnavailable failure, got %v (status=%s)", err, status)
	}

	// Immediate retry during backoff should return backoff error without calling provisioner again
	_, _, errBackoff := mgr.CheckAndAcquireRuntime(context.Background(), key)
	if errBackoff == nil || !strings.Contains(errBackoff.Error(), "backoff in effect") {
		t.Errorf("expected backoff error, got: %v", errBackoff)
	}
	if atomic.LoadInt64(&attemptCount) != 1 {
		t.Errorf("expected provisioner not called during backoff; attemptCount=%d", attemptCount)
	}

	// Wait for backoff to expire
	time.Sleep(60 * time.Millisecond)

	// Recover service
	shouldFail = false

	// Attempt 2: Now succeeds
	art, status, errSuccess := mgr.CheckAndAcquireRuntime(context.Background(), key)
	if errSuccess != nil || status != VersionSupported || art == nil {
		t.Fatalf("expected successful retry after backoff, got %v (status=%s)", errSuccess, status)
	}
	if art.ArtifactRef != "recovered-python-3.11" {
		t.Errorf("unexpected artifact ref: %s", art.ArtifactRef)
	}
	if atomic.LoadInt64(&attemptCount) != 2 {
		t.Errorf("expected attemptCount=2, got %d", attemptCount)
	}
}
