package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
)

// ExpiryReaper enforces the irreversible half of the timer story: when a
// sandbox's expires_at passes, it is torn down completely — app container,
// sidecar DB containers, their data volumes, the private network — and its row
// is left as a tombstone (status 'expired', destroyed_at set) so the user can
// still see it and recreate it. It shares NO code with IdleReaper by design.
type ExpiryReaper struct {
	store    *db.Store
	orch     *orchestrator.Orchestrator
	cfg      *config.Config
	logger   *slog.Logger
	interval time.Duration
}

// NewExpiryReaper constructs the destroyer worker.
func NewExpiryReaper(store *db.Store, orch *orchestrator.Orchestrator, cfg *config.Config, logger *slog.Logger, interval time.Duration) *ExpiryReaper {
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

		// Fencing Token & Immediate Expiration Validation:
		// Atomically transition from HIBERNATED -> EXPIRED only if lifecycle_version matches
		// and the sandbox is still hibernated with expired deadline.
		expiredOk, err := r.store.ExpireHibernatedSandbox(sb.ID, sb.LifecycleVersion, now)
		if err != nil {
			r.logger.Warn("expiry-reaper: checking expiration validity", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			continue
		}
		if !expiredOk {
			r.logger.Info("expiry-reaper: sandbox woke up or lifecycle version changed; stale expiration job aborted",
				slog.String("sandbox", sb.ID),
				slog.Int64("fenced_version", sb.LifecycleVersion))
			continue
		}

		// Runtime container teardown: container, sidecars, volumes, and network
		if r.orch != nil {
			if err := r.orch.TeardownSandbox(ctx, sb); err != nil {
				r.logger.Warn("expiry-reaper: teardown failed",
					slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			}
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
