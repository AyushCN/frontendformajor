package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ProvisionFunc defines the worker function that provisions a runtime artifact in an isolated container/builder.
// It must never install runtimes directly on the host.
type ProvisionFunc func(ctx context.Context, key RuntimeCacheKey) (*RuntimeArtifact, error)

type failureRecord struct {
	timestamp time.Time
	reason    string
}

// RuntimeAvailabilityManager coordinates runtime availability checks, caching,
// legacy runtime routing, and single-flight concurrent provisioning.
type RuntimeAvailabilityManager struct {
	cache          *SharedRuntimeCache
	legacyPolicy   *LegacyRuntimePolicy
	sfGroup        singleflight.Group
	logger         *slog.Logger
	provisionFn    ProvisionFunc
	failedMu       sync.RWMutex
	recentFailures map[string]failureRecord // key.Key() -> last failure info
	backoffDuration time.Duration
}

// NewRuntimeAvailabilityManager creates a new manager instance.
func NewRuntimeAvailabilityManager(cache *SharedRuntimeCache, policy *LegacyRuntimePolicy, logger *slog.Logger) *RuntimeAvailabilityManager {
	if cache == nil {
		cache = NewSharedRuntimeCache(0)
	}
	if policy == nil {
		policy = NewLegacyRuntimePolicy()
	}
	return &RuntimeAvailabilityManager{
		cache:           cache,
		legacyPolicy:    policy,
		logger:          logger,
		recentFailures:  make(map[string]failureRecord),
		backoffDuration: 5 * time.Second,
	}
}

// SetProvisionFunc sets the isolated provisioning executor.
func (m *RuntimeAvailabilityManager) SetProvisionFunc(fn ProvisionFunc) {
	m.provisionFn = fn
}

// SetBackoffDuration sets the transient failure backoff interval.
func (m *RuntimeAvailabilityManager) SetBackoffDuration(d time.Duration) {
	m.failedMu.Lock()
	defer m.failedMu.Unlock()
	m.backoffDuration = d
}

// Cache returns the underlying shared runtime cache.
func (m *RuntimeAvailabilityManager) Cache() *SharedRuntimeCache {
	return m.cache
}

// Policy returns the legacy runtime policy.
func (m *RuntimeAvailabilityManager) Policy() *LegacyRuntimePolicy {
	return m.legacyPolicy
}

// EvaluateRuntime inspects a language and requested version against policy, returning
// the version status, strategy ("railpack" or "dockerfile"), and any legacy image or reason.
func (m *RuntimeAvailabilityManager) EvaluateRuntime(language, version string) (VersionStatus, string, string) {
	return m.legacyPolicy.EvaluateVersion(language, version)
}

// CheckAvailability determines whether the requested runtime is already available in cache,
// requires provisioning, or is unsupported/incompatible.
func (m *RuntimeAvailabilityManager) CheckAvailability(key RuntimeCacheKey) (VersionStatus, *RuntimeArtifact, error) {
	// Validate OS and architecture
	if valid, reason := ValidateArchOS(key.OS, key.Architecture); !valid {
		return VersionIncompatible, nil, fmt.Errorf("runtime %s %s incompatible: %s", key.Language, key.Version, reason)
	}

	// 1. Check cache first
	if art, found := m.cache.Get(key); found {
		return VersionSupported, art, nil
	}

	// 2. Evaluate language and version policy
	status, strategy, legacyImgOrReason := m.legacyPolicy.EvaluateVersion(key.Language, key.Version)
	if status == VersionUnsupported {
		return VersionUnsupported, nil, fmt.Errorf("runtime version %s %s is unsupported: %s", key.Language, key.Version, legacyImgOrReason)
	}

	// 3. Check for active failure backoff
	k := key.Key()
	m.failedMu.RLock()
	failRec, hasFailed := m.recentFailures[k]
	backoff := m.backoffDuration
	m.failedMu.RUnlock()

	if hasFailed && time.Since(failRec.timestamp) < backoff {
		return VersionUnavailable, nil, fmt.Errorf("runtime %s %s provisioning recently failed: %s (backoff in effect)", key.Language, key.Version, failRec.reason)
	}

	// If legacy dockerfile container is approved and immediately ready
	if strategy == "dockerfile" && legacyImgOrReason != "" {
		return VersionProvisioningRequired, nil, nil
	}

	return VersionProvisioningRequired, nil, nil
}

// CheckAndAcquireRuntime checks whether the requested runtime is cached and ready,
// coordinates singleflight provisioning if required, and returns a leased runtime artifact.
func (m *RuntimeAvailabilityManager) CheckAndAcquireRuntime(
	ctx context.Context,
	key RuntimeCacheKey,
) (*RuntimeArtifact, VersionStatus, error) {
	// Validate OS and architecture
	if valid, reason := ValidateArchOS(key.OS, key.Architecture); !valid {
		return nil, VersionIncompatible, fmt.Errorf("runtime %s %s incompatible: %s", key.Language, key.Version, reason)
	}

	status, strategy, legacyImgOrReason := m.legacyPolicy.EvaluateVersion(key.Language, key.Version)
	if status == VersionUnsupported {
		return nil, VersionUnsupported, fmt.Errorf("runtime version %s %s is unsupported: %s", key.Language, key.Version, legacyImgOrReason)
	}

	// 1. Fast path: check cache first
	if art, found := m.cache.Get(key); found {
		m.cache.AcquireLease(key)
		return art, VersionSupported, nil
	}

	// 2. If legacy dockerfile container is approved and no custom provisioner is registered,
	// generate the approved legacy container artifact reference directly.
	if strategy == "dockerfile" && legacyImgOrReason != "" && m.provisionFn == nil {
		artifact := &RuntimeArtifact{
			Key:          key,
			ArtifactRef:  legacyImgOrReason,
			SizeBytes:    50 * 1024 * 1024, // approx 50MB base image
			CreatedAt:    time.Now().UTC(),
			LastUsedAt:   time.Now().UTC(),
			ActiveBuilds: 1,
			IsLegacy:     true,
			Status:       VersionSupported,
			Builder:      "dockerfile",
		}
		_ = m.cache.Put(artifact)
		return artifact, VersionSupported, nil
	}

	// 3. Check recent failure rate to avoid tight crash looping
	k := key.Key()
	m.failedMu.RLock()
	failRec, failedRecently := m.recentFailures[k]
	backoff := m.backoffDuration
	m.failedMu.RUnlock()

	if failedRecently && time.Since(failRec.timestamp) < backoff {
		return nil, VersionUnavailable, fmt.Errorf("runtime provisioning for %s %s recently failed: %s (backoff in effect)", key.Language, key.Version, failRec.reason)
	}

	// 4. Default railpack runtime image reference if no custom provisioning worker is set
	if m.provisionFn == nil {
		defaultRef := fmt.Sprintf("ghcr.io/railwayapp/railpack-runtime:%s-%s", key.Language, key.Version)
		if key.Version == "" {
			defaultRef = fmt.Sprintf("ghcr.io/railwayapp/railpack-runtime:%s-latest", key.Language)
		}
		artifact := &RuntimeArtifact{
			Key:          key,
			ArtifactRef:  defaultRef,
			SizeBytes:    100 * 1024 * 1024,
			CreatedAt:    time.Now().UTC(),
			LastUsedAt:   time.Now().UTC(),
			ActiveBuilds: 1,
			IsLegacy:     false,
			Status:       VersionSupported,
			Builder:      "railpack",
		}
		_ = m.cache.Put(artifact)
		return artifact, VersionSupported, nil
	}

	// 5. Singleflight provisioning with caller-cancellation resilience
	// Using DoChan allows the caller context to detach on cancellation without aborting
	// the shared worker or corrupting the cache.
	ch := m.sfGroup.DoChan(k, func() (any, error) {
		// Double check cache under singleflight lock
		if art, found := m.cache.Get(key); found {
			return art, nil
		}

		if m.logger != nil {
			m.logger.Info("provisioning runtime artifact",
				slog.String("language", key.Language),
				slog.String("version", key.Version),
				slog.String("key", k))
		}

		// Execute isolated worker in non-host container/BuildKit environment
		// Worker uses a detached background context with bounded timeout so caller cancellation
		// does not corrupt shared provisioning state.
		provCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		art, provErr := m.provisionFn(provCtx, key)
		if provErr != nil {
			m.RecordFailure(key, provErr)
			return nil, provErr
		}

		if art != nil {
			art.Status = VersionSupported
			_ = m.cache.Put(art)
			m.ClearFailure(key)
		}
		return art, nil
	})

	select {
	case <-ctx.Done():
		return nil, VersionUnavailable, fmt.Errorf("runtime acquisition cancelled: %w", ctx.Err())
	case res := <-ch:
		if res.Err != nil {
			return nil, VersionUnavailable, fmt.Errorf("runtime provisioning failed for %s %s: %w", key.Language, key.Version, res.Err)
		}

		art, ok := res.Val.(*RuntimeArtifact)
		if !ok || art == nil {
			return nil, VersionUnavailable, fmt.Errorf("runtime provisioning returned invalid artifact")
		}

		m.cache.AcquireLease(key)
		return art, VersionSupported, nil
	}
}

// ReleaseRuntime releases the active build lease on a runtime artifact.
func (m *RuntimeAvailabilityManager) ReleaseRuntime(key RuntimeCacheKey) {
	m.cache.ReleaseLease(key)
}

// RecordFailure records a provisioning failure for transient backoff tracking.
func (m *RuntimeAvailabilityManager) RecordFailure(key RuntimeCacheKey, err error) {
	if err == nil {
		return
	}
	m.failedMu.Lock()
	defer m.failedMu.Unlock()
	m.recentFailures[key.Key()] = failureRecord{
		timestamp: time.Now().UTC(),
		reason:    err.Error(),
	}
}

// ClearFailure clears any failure record for the specified key to allow immediate retry.
func (m *RuntimeAvailabilityManager) ClearFailure(key RuntimeCacheKey) {
	m.failedMu.Lock()
	defer m.failedMu.Unlock()
	delete(m.recentFailures, key.Key())
}

// GetFailure returns the timestamp and error message of the last failure if within backoff.
func (m *RuntimeAvailabilityManager) GetFailure(key RuntimeCacheKey) (time.Time, string, bool) {
	m.failedMu.RLock()
	defer m.failedMu.RUnlock()
	rec, ok := m.recentFailures[key.Key()]
	if !ok {
		return time.Time{}, "", false
	}
	return rec.timestamp, rec.reason, true
}
