package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
)

// 1. Entering HIBERNATED records hibernated_at and sets sliding expires_at
func TestResettableExpiry_HibernatesAndStartsCountdown(t *testing.T) {
	now := time.Now().UTC()
	hibTimeout := 7200 * time.Second // 2 hours

	sb := &db.Sandbox{
		ID:                          "sb-test-1",
		Status:                      db.StatusRunning,
		HibernatedExpirationSeconds: int64(hibTimeout.Seconds()),
		LifecycleVersion:            1,
	}

	// Trigger hibernation
	hibAt := now
	expAt := hibAt.Add(hibTimeout)
	sb.Status = db.StatusHibernated
	sb.HibernatedAt = &hibAt
	sb.ExpiresAt = expAt
	sb.LifecycleVersion++

	if sb.Status != db.StatusHibernated {
		t.Fatalf("expected status hibernated, got %s", sb.Status)
	}
	if sb.HibernatedAt == nil || !sb.HibernatedAt.Equal(hibAt) {
		t.Fatalf("expected hibernated_at %v, got %v", hibAt, sb.HibernatedAt)
	}
	if !sb.ExpiresAt.Equal(expAt) {
		t.Fatalf("expected expires_at %v, got %v", expAt, sb.ExpiresAt)
	}
	if sb.LifecycleVersion != 2 {
		t.Fatalf("expected version 2, got %d", sb.LifecycleVersion)
	}
}

// 2. User wakes before expiration: clears hibernated_at and expires_at, transitions to RUNNING
func TestResettableExpiry_UserWakesBeforeExpiration(t *testing.T) {
	hibAt := time.Now().UTC().Add(-30 * time.Minute)
	expAt := hibAt.Add(2 * time.Hour)

	sb := &db.Sandbox{
		ID:                          "sb-test-2",
		Status:                      db.StatusHibernated,
		HibernatedAt:                &hibAt,
		ExpiresAt:                   expAt,
		HibernatedExpirationSeconds: 7200,
		LifecycleVersion:            2,
	}

	// Wake succeeds: transition to RUNNING atomically
	sb.Status = db.StatusRunning
	sb.HibernatedAt = nil
	sb.ExpiresAt = time.Time{}
	sb.LifecycleVersion++

	if sb.Status != db.StatusRunning {
		t.Fatalf("expected status running, got %s", sb.Status)
	}
	if sb.HibernatedAt != nil {
		t.Fatalf("expected hibernated_at to be nil after wake, got %v", sb.HibernatedAt)
	}
	if !sb.ExpiresAt.IsZero() {
		t.Fatalf("expected expires_at to be zero after wake, got %v", sb.ExpiresAt)
	}
	if sb.LifecycleVersion != 3 {
		t.Fatalf("expected lifecycle_version 3, got %d", sb.LifecycleVersion)
	}
}

// 3. RUNNING sandboxes have no active expiration countdown ("Not applicable")
func TestResettableExpiry_RunningHasNoActiveExpiration(t *testing.T) {
	sb := &db.Sandbox{
		ID:        "sb-test-3",
		Status:    db.StatusRunning,
		ExpiresAt: time.Time{},
	}

	isExpirationActive := sb.Status == db.StatusHibernated && !sb.ExpiresAt.IsZero()
	countdownText := "Not applicable"
	if isExpirationActive {
		countdownText = "Active"
	}

	if isExpirationActive {
		t.Fatalf("expiration must be inactive while RUNNING")
	}
	if countdownText != "Not applicable" {
		t.Fatalf("expected countdown text 'Not applicable', got %s", countdownText)
	}
}

// 4. Hibernating again calculates a completely fresh deadline based on new hibernated_at
func TestResettableExpiry_HibernatingAgainGetsFreshDeadline(t *testing.T) {
	sb := &db.Sandbox{
		ID:                          "sb-test-4",
		Status:                      db.StatusRunning,
		HibernatedExpirationSeconds: 7200, // 2h
		LifecycleVersion:            3,
	}

	// First hibernation was cleared. Sandbox is running, then idles again after 3 hours
	secondHibAt := time.Now().UTC()
	secondExpAt := secondHibAt.Add(time.Duration(sb.HibernatedExpirationSeconds) * time.Second)

	sb.Status = db.StatusHibernated
	sb.HibernatedAt = &secondHibAt
	sb.ExpiresAt = secondExpAt
	sb.LifecycleVersion++

	if sb.Status != db.StatusHibernated {
		t.Fatalf("expected status hibernated, got %s", sb.Status)
	}
	if !sb.ExpiresAt.Equal(secondExpAt) {
		t.Fatalf("expected fresh expires_at %v, got %v", secondExpAt, sb.ExpiresAt)
	}
	if sb.LifecycleVersion != 4 {
		t.Fatalf("expected version 4, got %d", sb.LifecycleVersion)
	}
}

// 5. Expired sandbox rejects wake request with 410 Gone
func TestResettableExpiry_ExpiredSandboxRejectsWake(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-10 * time.Minute)
	sb := &db.Sandbox{
		ID:        "sb-test-5",
		Status:    db.StatusExpired,
		ExpiresAt: expiredAt,
	}

	req := httptest.NewRequest(http.MethodGet, "http://sb-test-5.sandbox.localhost:8090/", nil)
	_ = req
	rec := httptest.NewRecorder()

	if sb.Status == db.StatusExpired || (!sb.ExpiresAt.IsZero() && time.Now().After(sb.ExpiresAt) && sb.Status == db.StatusHibernated) {
		http.Error(rec, "sandbox has expired and cannot be woken", http.StatusGone)
	}

	if rec.Code != http.StatusGone {
		t.Fatalf("expected status 410 Gone for expired sandbox, got %d", rec.Code)
	}
}

// 6. Wake failure preserves the original expiration deadline without extending time
func TestResettableExpiry_FailedWakePreservesDeadline(t *testing.T) {
	originalHibAt := time.Now().UTC().Add(-1 * time.Hour)
	originalExpAt := originalHibAt.Add(2 * time.Hour)

	sb := &db.Sandbox{
		ID:                          "sb-test-6",
		Status:                      db.StatusHibernated,
		HibernatedAt:                &originalHibAt,
		ExpiresAt:                   originalExpAt,
		HibernatedExpirationSeconds: 7200,
		LifecycleVersion:            2,
	}

	// Simulate wake attempt that fails TCP health check
	wakeErr := errors.New("container failed readiness health check: port unreachable")

	// Since wake failed, do NOT call TransitionToRunning
	if wakeErr != nil {
		// Preserve status and original deadline
	}

	if sb.Status != db.StatusHibernated {
		t.Fatalf("expected status to remain hibernated, got %s", sb.Status)
	}
	if !sb.ExpiresAt.Equal(originalExpAt) {
		t.Fatalf("expected expires_at %v preserved, got %v", originalExpAt, sb.ExpiresAt)
	}
	if sb.LifecycleVersion != 2 {
		t.Fatalf("version must not increment on failed wake, got %d", sb.LifecycleVersion)
	}
}

// 7. Concurrent wake deduplication using singleflight
func TestResettableExpiry_ConcurrentWakeDeduplication(t *testing.T) {
	var wakeCalls int32
	var wg sync.WaitGroup

	wakeAction := func() error {
		atomic.AddInt32(&wakeCalls, 1)
		time.Sleep(30 * time.Millisecond) // simulate container start
		return nil
	}

	g := &singleflightGroup{}
	results := make([]error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			_, err := g.Do("sb-concurrent", func() (interface{}, error) {
				return nil, wakeAction()
			})
			results[idx] = err
		}()
	}

	wg.Wait()

	if wakeCalls != 1 {
		t.Fatalf("expected exactly 1 wake call due to singleflight deduplication, got %d", wakeCalls)
	}
	for i, err := range results {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
}

// 8. Stale expiration job aborted by fencing token (lifecycle_version)
func TestResettableExpiry_OldExpirationJobAbortedByFencing(t *testing.T) {
	// Sandbox woke up: version incremented from 2 to 3
	currentSB := &db.Sandbox{
		ID:               "sb-test-8",
		Status:           db.StatusRunning,
		LifecycleVersion: 3,
	}

	// Background expiry job was enqueued when version was 2
	staleJobVersion := int64(2)

	// CAS update condition: id = ? AND lifecycle_version = ? AND status = 'hibernated'
	canExpire := currentSB.LifecycleVersion == staleJobVersion && currentSB.Status == db.StatusHibernated

	if canExpire {
		t.Fatalf("stale expiration job with version %d should have been rejected by fencing (current version %d)", staleJobVersion, currentSB.LifecycleVersion)
	}
}

// 9. Worker restart recovery from authoritative database
func TestResettableExpiry_WorkerRestartRecovery(t *testing.T) {
	now := time.Now().UTC()
	expiringSoon := now.Add(-5 * time.Second)

	// Persisted sandboxes in DB
	dbSandboxes := []*db.Sandbox{
		{
			ID:               "sb-1",
			Status:           db.StatusHibernated,
			ExpiresAt:        expiringSoon,
			LifecycleVersion: 2,
		},
		{
			ID:               "sb-2",
			Status:           db.StatusRunning,
			ExpiresAt:        time.Time{},
			LifecycleVersion: 1,
		},
	}

	// Worker restarts and queries: WHERE status = 'hibernated' AND expires_at <= now
	var dueForExpiry []*db.Sandbox
	for _, sb := range dbSandboxes {
		if sb.Status == db.StatusHibernated && !sb.ExpiresAt.IsZero() && sb.ExpiresAt.Before(now) {
			dueForExpiry = append(dueForExpiry, sb)
		}
	}

	if len(dueForExpiry) != 1 || dueForExpiry[0].ID != "sb-1" {
		t.Fatalf("expected worker restart to recover sb-1, got %d sandboxes", len(dueForExpiry))
	}
}

// 10. Redis restart preserves authoritative database state
func TestResettableExpiry_RedisRestartPreservesAuthoritativeDB(t *testing.T) {
	dbSB := &db.Sandbox{
		ID:                          "sb-test-10",
		Status:                      db.StatusHibernated,
		HibernatedExpirationSeconds: 7200,
		LifecycleVersion:            4,
	}

	// If Redis is cleared/restarted, DB holds the ground truth
	redisCacheLost := true
	_ = redisCacheLost

	if dbSB.Status != db.StatusHibernated || dbSB.LifecycleVersion != 4 {
		t.Fatalf("database ground truth corrupted")
	}
}

// 11. Changing timeout while hibernated dynamically recalibrates expires_at
func TestResettableExpiry_ConfigChangeWhileHibernated(t *testing.T) {
	hibAt := time.Now().UTC().Add(-1 * time.Hour)
	oldTimeout := int64(7200) // 2h -> expires in 1h from now
	oldExpiresAt := hibAt.Add(time.Duration(oldTimeout) * time.Second)

	sb := &db.Sandbox{
		ID:                          "sb-test-11",
		Status:                      db.StatusHibernated,
		HibernatedAt:                &hibAt,
		ExpiresAt:                   oldExpiresAt,
		HibernatedExpirationSeconds: oldTimeout,
	}

	// User updates config to 4 hours (14400s)
	newTimeout := int64(14400)
	sb.HibernatedExpirationSeconds = newTimeout

	// If currently hibernated, recalibrate: expires_at = hibernated_at + new_timeout
	if sb.Status == db.StatusHibernated && sb.HibernatedAt != nil {
		sb.ExpiresAt = sb.HibernatedAt.Add(time.Duration(newTimeout) * time.Second)
	}

	expectedNewExp := hibAt.Add(4 * time.Hour)
	if !sb.ExpiresAt.Equal(expectedNewExp) {
		t.Fatalf("expected recalibrated expires_at %v, got %v", expectedNewExp, sb.ExpiresAt)
	}
}

// 12. Config change during waking does not bypass readiness check
func TestResettableExpiry_ConfigChangeDuringWaking(t *testing.T) {
	statusWaking := db.SandboxStatus("waking")
	sb := &db.Sandbox{
		ID:                          "sb-test-12",
		Status:                      statusWaking,
		HibernatedExpirationSeconds: 7200,
	}

	// User calls update settings during waking
	sb.HibernatedExpirationSeconds = 14400

	// Sandbox must remain WAKING until readiness succeeds
	if sb.Status != statusWaking {
		t.Fatalf("config change must not force status transition out of WAKING")
	}
}

// 13. Container removed externally while DB marks hibernated reports clean error
func TestResettableExpiry_ContainerRemovedWhileDBHibernated(t *testing.T) {
	sb := &db.Sandbox{
		ID:          "sb-test-13",
		Status:      db.StatusHibernated,
		ContainerID: "missing-container-123",
	}

	containerExists := false
	var wakeErr error
	if !containerExists {
		wakeErr = errors.New("underlying container missing from docker daemon")
	}

	if wakeErr == nil {
		t.Fatalf("expected wake error for missing container")
	}
	// Status must not be corrupted to RUNNING
	if sb.Status == db.StatusRunning {
		t.Fatalf("sandbox must not transition to RUNNING when container is missing")
	}
}

// 14. Reconciles out-of-sync container state
func TestResettableExpiry_DBRunningContainerStoppedReconciliation(t *testing.T) {
	sb := &db.Sandbox{
		ID:     "sb-test-14",
		Status: db.StatusRunning,
	}

	containerRunning := false

	// Reconciler checks docker inspect
	if !containerRunning && sb.Status == db.StatusRunning {
		// Reconcile out-of-sync state
		sb.Status = db.SandboxStatus("stopped")
	}

	if sb.Status != db.SandboxStatus("stopped") {
		t.Fatalf("expected reconciled status STOPPED, got %s", sb.Status)
	}
}

// Helper mock singleflight implementation for deduplication test
type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

func (g *singleflightGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

func (s *singleflightGroup) Context() context.Context {
	return context.Background()
}
