package reaper

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
)

type mockExpiryStore struct {
	dueSandboxes []*db.Sandbox
	listErr      error

	expiredCalls []struct {
		id      string
		version int64
		now     time.Time
	}
	expiredSuccess bool
	expiredErr     error
}

func (m *mockExpiryStore) ListDuedExpiredSandboxes(now time.Time) ([]*db.Sandbox, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.dueSandboxes, nil
}

func (m *mockExpiryStore) ExpireHibernatedSandbox(id string, lifecycleVersion int64, now time.Time) (bool, error) {
	m.expiredCalls = append(m.expiredCalls, struct {
		id      string
		version int64
		now     time.Time
	}{id: id, version: lifecycleVersion, now: now})
	return m.expiredSuccess, m.expiredErr
}

type mockExpiryOrchestrator struct {
	teardownCalls []*db.Sandbox
	teardownErr   error
	sweepCalls    []string
	sweepErr      error
}

func (m *mockExpiryOrchestrator) TeardownSandbox(ctx context.Context, sb *db.Sandbox) error {
	m.teardownCalls = append(m.teardownCalls, sb)
	return m.teardownErr
}

func (m *mockExpiryOrchestrator) SweepSandboxContainers(ctx context.Context, sandboxID string) error {
	m.sweepCalls = append(m.sweepCalls, sandboxID)
	return m.sweepErr
}

func TestExpiryReaper_TeardownFailureDoesNotExpireDatabaseRow(t *testing.T) {
	sb := &db.Sandbox{
		ID:               "sb-test-1",
		LifecycleVersion: 3,
		Status:           db.StatusHibernated,
	}

	store := &mockExpiryStore{
		dueSandboxes:   []*db.Sandbox{sb},
		expiredSuccess: true,
	}
	orch := &mockExpiryOrchestrator{
		teardownErr: errors.New("docker daemon connection timeout"),
	}

	cfg := &config.Config{SandboxWorkDir: t.TempDir()}
	r := NewExpiryReaper(store, orch, cfg, slog.Default(), 1*time.Second)

	// Run single tick
	r.tick(context.Background())

	// Teardown was attempted
	if len(orch.teardownCalls) != 1 {
		t.Fatalf("expected 1 teardown attempt, got %d", len(orch.teardownCalls))
	}

	// Because teardown failed, the DB row must NOT have been marked expired!
	if len(store.expiredCalls) != 0 {
		t.Fatalf("expected 0 calls to ExpireHibernatedSandbox on teardown failure, got %d", len(store.expiredCalls))
	}
}

func TestExpiryReaper_SuccessfulTeardownExpiresRowWithFencing(t *testing.T) {
	sb := &db.Sandbox{
		ID:               "sb-test-2",
		LifecycleVersion: 5,
		Status:           db.StatusHibernated,
	}

	store := &mockExpiryStore{
		dueSandboxes:   []*db.Sandbox{sb},
		expiredSuccess: true,
	}
	orch := &mockExpiryOrchestrator{
		teardownErr: nil,
	}

	cfg := &config.Config{SandboxWorkDir: t.TempDir()}
	r := NewExpiryReaper(store, orch, cfg, slog.Default(), 1*time.Second)

	r.tick(context.Background())

	if len(orch.teardownCalls) != 1 {
		t.Fatalf("expected 1 teardown attempt, got %d", len(orch.teardownCalls))
	}
	if len(store.expiredCalls) != 1 {
		t.Fatalf("expected 1 call to ExpireHibernatedSandbox, got %d", len(store.expiredCalls))
	}
	if store.expiredCalls[0].id != sb.ID {
		t.Errorf("expected id %s, got %s", sb.ID, store.expiredCalls[0].id)
	}
	if store.expiredCalls[0].version != sb.LifecycleVersion {
		t.Errorf("expected version %d, got %d", sb.LifecycleVersion, store.expiredCalls[0].version)
	}
}

func TestExpiryReaper_VersionMismatchAbortsStaleJob(t *testing.T) {
	sb := &db.Sandbox{
		ID:               "sb-test-3",
		LifecycleVersion: 7,
		Status:           db.StatusHibernated,
	}

	store := &mockExpiryStore{
		dueSandboxes:   []*db.Sandbox{sb},
		expiredSuccess: false, // Woken up or version incremented concurrently!
	}
	orch := &mockExpiryOrchestrator{
		teardownErr: nil,
	}

	cfg := &config.Config{SandboxWorkDir: t.TempDir()}
	r := NewExpiryReaper(store, orch, cfg, slog.Default(), 1*time.Second)

	r.tick(context.Background())

	if len(store.expiredCalls) != 1 {
		t.Fatalf("expected 1 call to ExpireHibernatedSandbox, got %d", len(store.expiredCalls))
	}
}
