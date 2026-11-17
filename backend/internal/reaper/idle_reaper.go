// Package reaper holds the background workers that enforce the two timers the
// product sells to users. The files here are deliberately kept in THREE
// separate files: the idle reaper's action (hibernate) is fully reversible,
// the expiry reaper's action (destroy) is not, and warnings are a third, side
// effect-free concern. Merge them only over your strong objection.
package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/api-sandbox-links/backend/internal/cache"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
)

// IdleReaper hibernates (stops) running containers that have received no
// request within their idle timeout. Reversible: the wake middleware restarts
// the container on the next request. Hibernation never deletes anything.
type IdleReaper struct {
	store    *db.Store
	cache    *cache.Redis
	orch     *orchestrator.Orchestrator
	logger   *slog.Logger
	interval time.Duration
}

// NewIdleReaper constructs the idle timer worker.
func NewIdleReaper(store *db.Store, cache *cache.Redis, orch *orchestrator.Orchestrator, logger *slog.Logger, interval time.Duration) *IdleReaper {
	return &IdleReaper{store: store, cache: cache, orch: orch, logger: logger, interval: interval}
}

// Run ticks every interval and hibernates any overdue running sandbox. It is
// cancellable via ctx (the server stops it cleanly on shutdown).
func (r *IdleReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.tick(ctx) // check immediately on boot too
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick is one pass: consider every sandbox currently marked "running".
func (r *IdleReaper) tick(ctx context.Context) {
	active, err := r.store.ListActiveSandboxes()
	if err != nil {
		r.logger.Warn("idle-reaper: listing sandboxes", slog.String("err", err.Error()))
		return
	}
	for _, sb := range active {
		// Only running containers can hibernate; queued/building ones have no
		// container that is safe to stop yet.
		if sb.Status != db.StatusRunning || sb.ContainerID == "" {
			continue
		}

		// 1. Active Operation Protection: Temporary bounded tasks (builds, git ops,
		// migrations) temporarily protect from hibernation.
		if op, ok := r.cache.GetActiveOperation(ctx, sb.ID); ok {
			r.logger.Debug("idle-reaper: active operation in progress, deferring hibernation",
				slog.String("sandbox", sb.ID),
				slog.String("op_type", op.Type),
				slog.String("user_id", op.UserID))
			continue
		}

		// 2. Multi-user Activity Tracking: If ANY authorized user is actively working,
		// do not hibernate.
		if r.cache.HasActiveUserActivity(ctx, sb.ID) {
			continue
		}

		// 3. Hibernation Warning & Grace Period:
		idleTTL := r.cache.GetIdleTTL(ctx, sb.ID)
		if idleTTL > 0 {
			// Approaching hibernation warning window (within 2 minutes)
			if idleTTL <= 2*time.Minute {
				_ = r.cache.SetHibernationWarning(ctx, sb.ID, 2*time.Minute)
			}
			continue
		}

		// Idle TTL has lapsed: enter or evaluate grace period
		if !r.cache.InGracePeriod(ctx, sb.ID) {
			graceDur := 60 * time.Second
			// Ensure grace period cannot bypass maximum sandbox lifetime or expiration
			if !sb.ExpiresAt.IsZero() && time.Now().Add(graceDur).After(sb.ExpiresAt) {
				graceDur = time.Until(sb.ExpiresAt)
				if graceDur <= 0 {
					continue // ExpiryReaper will permanently destroy it
				}
			}
			_ = r.cache.SetGracePeriod(ctx, sb.ID, graceDur)
			r.logger.Info("idle-reaper: sandbox entered hibernation grace period",
				slog.String("sandbox", sb.ID),
				slog.Duration("grace_duration", graceDur))
			continue
		}

		// Grace period has elapsed with no activity reset: proceed to hibernate.
		if err := r.orch.StopContainer(ctx, sb.ContainerID); err != nil {
			r.logger.Warn("idle-reaper: stopping container", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
			continue
		}
		if err := r.cache.Clear(ctx, sb.ID); err != nil {
			r.logger.Debug("idle-reaper: clearing marker", slog.String("sandbox", sb.ID))
		}
		r.orch.StopStatsCollector(sb.ID)

		now := time.Now().UTC()
		hibExpiration := sb.HibernatedExpirationSeconds
		if hibExpiration <= 0 {
			hibExpiration = 7200 // default 2 hours
		}
		expiresAt := now.Add(time.Duration(hibExpiration) * time.Second)

		if err := r.store.TransitionToHibernated(sb.ID, now, expiresAt); err != nil {
			r.logger.Warn("idle-reaper: marking hibernated", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		}
		r.logger.Info("idle-reaper: hibernated sandbox with sliding expiration countdown",
			slog.String("sandbox", sb.ID),
			slog.Time("hibernated_at", now),
			slog.Time("expires_at", expiresAt))
	}
}
