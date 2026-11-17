package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
)

// ExpiryStore defines database queries needed by the expiry reaper.
type ExpiryStore interface {
	ListDuedExpiredSandboxes(now time.Time) ([]*db.Sandbox, error)
	ExpireHibernatedSandbox(id string, lifecycleVersion int64, now time.Time) (bool, error)
}

// ExpiryOrchestrator defines the runtime teardown operations needed by the expiry reaper.
type ExpiryOrchestrator interface {
	TeardownSandbox(ctx context.Context, sb *db.Sandbox) error
	SweepSandboxContainers(ctx context.Context, sandboxID string) error
}

// ExpiryReaper enforces the irreversible half of the timer story: when a
// sandbox's expires_at passes, it is torn down completely — app container,
// sidecar DB containers, their data volumes, the private network — and its row
// is left as a tombstone (status 'expired', destroyed_at set) so the user can
// still see it and recreate it. It shares NO code with IdleReaper by design.
type ExpiryReaper struct {
	store    ExpiryStore
	orch     ExpiryOrchestrator
	cfg      *config.Config
	logger   *slog.Logger
	interval time.Duration
}

// NewExpiryReaper constructs the destroyer worker.
func NewExpiryReaper(store ExpiryStore, orch ExpiryOrchestrator, cfg *config.Config, logger *slog.Logger, interval time.Duration) *ExpiryReaper {
	return &ExpiryReaper{store: store, orch: orch, cfg: cfg, logger: logger, interval: interval}
}

// Run ticks every interval and destroys every due sandbox. Cancellable via ctx.
func (r *ExpiryReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick finds due-destroyed sandboxes and permanently removes their runtime.
func (r *ExpiryReaper) tick(ctx context.Context) {
	due, err := r.store.ListDuedExpiredSandboxes(time.Now())
	if err != nil {
		r.logger.Warn("expiry-reaper: listing due sandboxes", slog.String("err", err.Error()))
		return
	}
	for _, sb := range due {
		now := time.Now().UTC()

		// Requirement 8: Do not mark a sandbox as cleaned until runtime cleanup is confirmed.
		// First perform container, sidecar volume, and network teardown.
		if r.orch != nil {
			if err := r.orch.TeardownSandbox(ctx, sb); err != nil {
				r.logger.Warn("expiry-reaper: runtime teardown failed; will retry cleanup on next tick",
					slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
				continue // DO NOT mark expired; retry on next tick
			}
		}

		// Fencing Token & Authoritative Expiration Commit:
		// Atomically transition from HIBERNATED -> EXPIRED only if lifecycle_version matches
		// and the sandbox is still hibernated with expired deadline.
		expiredOk, err := r.store.ExpireHibernatedSandbox(sb.ID, sb.LifecycleVersion, now)
		if err != nil {
			r.logger.Warn("expiry-reaper: checking expiration validity in database", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			continue
		}
		if !expiredOk {
			r.logger.Info("expiry-reaper: sandbox woke up or lifecycle version changed; stale expiration aborted",
				slog.String("sandbox", sb.ID),
				slog.Int64("fenced_version", sb.LifecycleVersion))
			continue
		}

		// Data Preservation: Separate runtime lifecycle from workspace data retention.
		// On-disk workspace data is preserved for the configured retention period post-expiration.
		retentionPeriod := time.Duration(sb.WorkspaceRetentionSeconds) * time.Second
		if retentionPeriod <= 0 {
			retentionPeriod = 7 * 24 * time.Hour // default 7 days
		}
		if sb.DestroyedAt != nil && time.Since(*sb.DestroyedAt) > retentionPeriod {
			if err := orchestrator.RemoveSandboxWorkDir(r.cfg.SandboxWorkDir, sb.Subdomain); err != nil {
				r.logger.Warn("expiry-reaper: removing retention-expired work dir",
					slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			}
		}

		// Sweep for lingering containers
		if r.orch != nil {
			if err := r.orch.SweepSandboxContainers(ctx, sb.ID); err != nil {
				r.logger.Warn("expiry-reaper: post-tombstone container sweep",
					slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			}
		}
		r.logger.Info("expiry-reaper: expired runtime container successfully torn down (workspace preserved)",
			slog.String("sandbox", sb.ID), slog.String("subdomain", sb.Subdomain))
	}
}
