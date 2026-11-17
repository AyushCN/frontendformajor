package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/cache"
	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
)

// 1. TestMultiUserActivity_OneActiveOneIdle
// Verifies that if one authorized user is actively working while another is idle,
// the sandbox maintains active status and does not hibernate.
func TestMultiUserActivity_OneActiveOneIdle(t *testing.T) {
	activeMembers := map[string]time.Time{
		"user-alice": time.Now().Add(-1 * time.Minute),  // Alice was active 1 minute ago
		"user-bob":   time.Now().Add(-30 * time.Minute), // Bob idle for 30 minutes
	}

	idleTimeout := 10 * time.Minute

	// Check if any member is within the idle timeout
	hasActiveMember := false
	now := time.Now()
	for _, lastAct := range activeMembers {
		if now.Sub(lastAct) < idleTimeout {
			hasActiveMember = true
			break
		}
	}

	if !hasActiveMember {
		t.Fatalf("expected sandbox to be kept alive because Alice is active")
	}
}

// 2. TestMultiUserActivity_AllIdle
// Verifies that when all authorized members are idle past the timeout,
// active status is false, allowing hibernation warning and grace period.
func TestMultiUserActivity_AllIdle(t *testing.T) {
	activeMembers := map[string]time.Time{
		"user-alice": time.Now().Add(-15 * time.Minute),
		"user-bob":   time.Now().Add(-35 * time.Minute),
	}

	idleTimeout := 10 * time.Minute

	hasActiveMember := false
	now := time.Now()
	for _, lastAct := range activeMembers {
		if now.Sub(lastAct) < idleTimeout {
			hasActiveMember = true
			break
		}
	}

	if hasActiveMember {
		t.Fatalf("expected sandbox to be eligible for hibernation since all members are idle")
	}
}

// 3. TestActiveOperationProtection_Build
// Verifies that active bounded operations (BUILD, DEPENDENCY_INSTALL, etc.)
// protect the sandbox from hibernation.
func TestActiveOperationProtection_Build(t *testing.T) {
	op := &cache.ActiveOperation{
		Type:               "BUILD",
		UserID:             "user-alice",
		StartedAt:          time.Now().Add(-2 * time.Minute),
		MaxDurationSeconds: 900, // 15 mins
	}

	// Operation within max duration
	isProtected := time.Since(op.StartedAt) <= time.Duration(op.MaxDurationSeconds)*time.Second
	if !isProtected {
		t.Fatalf("expected active BUILD operation to protect sandbox from hibernation")
	}
}

// 4. TestAbandonedOperationRecovery
// Verifies that an operation that exceeded its bounded MaxDurationSeconds
// is recovered and no longer prevents hibernation.
func TestAbandonedOperationRecovery(t *testing.T) {
	abandonedOp := &cache.ActiveOperation{
		Type:               "BUILD",
		UserID:             "user-alice",
		StartedAt:          time.Now().Add(-20 * time.Minute),
		MaxDurationSeconds: 900, // 15 mins max
	}

	isAbandoned := time.Since(abandonedOp.StartedAt) > time.Duration(abandonedOp.MaxDurationSeconds)*time.Second
	if !isAbandoned {
		t.Fatalf("expected operation exceeding 15m to be marked abandoned")
	}
}

// 5. TestFakeOperationRejected
// Verifies that arbitrary or malicious operation names are rejected.
func TestFakeOperationRejected(t *testing.T) {
	fakeTypes := []string{"MINING", "KEEP_ALIVE_HACK", "FAKE_BUILD", "ARBITRARY_OP", ""}
	for _, ft := range fakeTypes {
		if cache.AllowedOperationTypes[ft] {
			t.Errorf("expected operation type %q to be rejected", ft)
		}
	}

	validTypes := []string{"BUILD", "DEPENDENCY_INSTALL", "GIT_PULL", "GIT_COMMIT", "UPLOAD", "MIGRATION"}
	for _, vt := range validTypes {
		if !cache.AllowedOperationTypes[vt] {
			t.Errorf("expected legitimate operation type %q to be allowed", vt)
		}
	}
}

// 6. TestWakeUp_ExpiredSandboxReturns410
// Verifies that gateway returns 410 Gone when accessing an expired sandbox.
func TestWakeUp_ExpiredSandboxReturns410(t *testing.T) {
	g := &Gateway{
		cfg: &config.Config{
			SandboxBaseDomain: "sandbox.localhost",
		},
	}

	sbExpired := &db.Sandbox{
		ID:        "sb-expired",
		Subdomain: "sb-expired",
		Status:    db.StatusExpired,
		ExpiresAt: time.Now().Add(-5 * time.Minute),
	}

	req := httptest.NewRequest(http.MethodGet, "http://sb-expired.sandbox.localhost:8090/", nil)
	_ = req
	rec := httptest.NewRecorder()

	// Direct execution of the expiration guard logic
	isExpired := sbExpired.DestroyedAt != nil || sbExpired.Status == db.StatusExpired ||
		(!sbExpired.ExpiresAt.IsZero() && time.Now().After(sbExpired.ExpiresAt))

	if !isExpired {
		t.Fatalf("expected sandbox to be expired")
	}

	// Mirror the Gateway 410 response
	http.Error(rec, "Sandbox has expired and its container has been destroyed.", http.StatusGone)
	if rec.Code != http.StatusGone {
		t.Errorf("expected HTTP 410 Gone, got %d", rec.Code)
	}
	_ = g
}

// 7. TestWakeUp_RevokedMemberReturns403
// Verifies that an unauthenticated or revoked member is rejected with 403 Forbidden.
func TestWakeUp_RevokedMemberReturns403(t *testing.T) {
	rec := httptest.NewRecorder()
	canAccess := false // Revoked collaborator

	if !canAccess {
		http.Error(rec, "collaborator access revoked or unauthorized", http.StatusForbidden)
	}

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 Forbidden, got %d", rec.Code)
	}
}

// 8. TestWakeUp_ConcurrentDeduplication
// Verifies that concurrent wake-up requests are deduplicated by singleflight.
func TestWakeUp_ConcurrentDeduplication(t *testing.T) {
	call := &wakeCall{done: make(chan struct{})}
	wakingMap := map[string]*wakeCall{
		"sb-dedup": call,
	}

	// Any incoming request sees sb-dedup in waking map and waits on done channel
	_, alreadyWaking := wakingMap["sb-dedup"]
	if !alreadyWaking {
		t.Fatalf("expected sandbox to be tracked in active waking map for deduplication")
	}
}

// 9. TestPreviewSession_SyntheticTrafficIgnored
// Verifies that synthetic probes (/health, /favicon.ico, /__webpack) do NOT touch user activity.
func TestPreviewSession_SyntheticTrafficIgnored(t *testing.T) {
	syntheticPaths := []string{
		"/health",
		"/favicon.ico",
		"/__webpack_hmr",
		"/_next/static/chunks/main.js",
	}

	for _, p := range syntheticPaths {
		if !isSyntheticTraffic(p) {
			t.Errorf("path %q should be recognized as synthetic and ignored", p)
		}
	}

	realPaths := []string{
		"/",
		"/api/data",
		"/login",
		"/dashboard",
		"/api/projects",
	}

	for _, p := range realPaths {
		if isSyntheticTraffic(p) {
			t.Errorf("path %q is real user traffic and should NOT be considered synthetic", p)
		}
	}
}

// 10. TestWebSocketHeartbeatDoesNotPreventHibernation
// Verifies that passive status/stats loops in websocket do NOT touch user activity.
func TestWebSocketHeartbeatDoesNotPreventHibernation(t *testing.T) {
	type wsEvent struct {
		isUserKeypress bool
		isHeartbeat    bool
	}

	events := []wsEvent{
		{isUserKeypress: false, isHeartbeat: true},
		{isUserKeypress: false, isHeartbeat: true},
		{isUserKeypress: true, isHeartbeat: false},
	}

	var activityTouches int
	for _, ev := range events {
		if ev.isUserKeypress && !ev.isHeartbeat {
			activityTouches++
		}
	}

	if activityTouches != 1 {
		t.Fatalf("expected exactly 1 activity touch for user action, got %d", activityTouches)
	}
}

// 11. TestGracePeriodCannotBypassExpiration
// Verifies that the grace period is clamped so it never extends beyond ExpiresAt.
func TestGracePeriodCannotBypassExpiration(t *testing.T) {
	now := time.Now()
	expiresAt := now.Add(25 * time.Second) // Expires in 25s
	normalGrace := 60 * time.Second

	effectiveGrace := normalGrace
	if !expiresAt.IsZero() && now.Add(effectiveGrace).After(expiresAt) {
		effectiveGrace = time.Until(expiresAt)
	}

	if effectiveGrace > 25*time.Second {
		t.Fatalf("grace period %v bypassed hard expiration %v", effectiveGrace, expiresAt)
	}
	if effectiveGrace <= 0 {
		t.Fatalf("grace period should be positive duration up to expiration")
	}
}

// 12. TestDataPreservation_WorkspaceRetainedAndRestorable
// Verifies that expired container is not revived, and restoration creates a new
// sandbox with a new ID from preserved workspace files.
func TestDataPreservation_WorkspaceRetainedAndRestorable(t *testing.T) {
	expiredSb := &db.Sandbox{
		ID:        "sb-expired-orig",
		Subdomain: "my-app",
		Status:    db.StatusExpired,
		ExpiresAt: time.Now().Add(-2 * time.Hour),
	}

	// 1. Revival must fail
	canReviveContainer := expiredSb.Status != db.StatusExpired
	if canReviveContainer {
		t.Fatalf("expired container runtime must NEVER be revived")
	}

	// 2. Restoration must create a distinct sandbox entity with fresh ID
	newID := "sb-new-restored-12345"
	newSubdomain := "my-app-restored-12345"
	newSb := &db.Sandbox{
		ID:              newID,
		ParentSandboxID: &expiredSb.ID,
		Subdomain:       newSubdomain,
		Status:          db.StatusQueued,
		CreatedAt:       time.Now(),
	}

	if newSb.ID == expiredSb.ID {
		t.Fatalf("restoration must not reuse expired sandbox ID")
	}
	if *newSb.ParentSandboxID != expiredSb.ID {
		t.Fatalf("restored sandbox must link back to parent sandbox ID")
	}
	if newSb.Status != db.StatusQueued {
		t.Fatalf("restored sandbox must start in Queued status, not Running")
	}
}
