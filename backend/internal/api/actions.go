package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
	"github.com/google/uuid"
)

// extendLifetimeRequest is the body of POST /{id}/actions/extend.
type extendLifetimeRequest struct {
	AdditionalSeconds int64 `json:"additional_seconds"`
}

// handleListDeployments returns the sandbox's deploy/build history.
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}
	rows, err := s.store.ListDeployments(sb.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list deployments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": rows})
}

// handleExtendLifetime pushes expires_at forward by the requested amount but
// never past the hard cap (MaxLifetimeSeconds from original creation), and
// recomputes lifetime_seconds so proportional warnings stay meaningful.
func (s *Server) handleExtendLifetime(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleEditor)
	if !ok {
		return
	}
	if sb.DestroyedAt != nil || sb.Status == db.StatusExpired || (!sb.ExpiresAt.IsZero() && time.Now().After(sb.ExpiresAt)) {
		writeErr(w, http.StatusConflict, "this sandbox has expired and cannot be extended")
		return
	}
	var req extendLifetimeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "additional_seconds is required")
		return
	}
	if req.AdditionalSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, "additional_seconds must be positive")
		return
	}

	// The cap is "lifetime + extensions", measured from the same instant the
	// lifetime is. Since that instant is now activation rather than creation, a
	// slow build no longer eats into the user's extension allowance.
	clockStart := sb.CreatedAt
	if sb.ActivatedAt != nil {
		clockStart = *sb.ActivatedAt
	}
	softCap := clockStart.Add(time.Duration(s.maxLifetime()) * time.Second)
	newExpiry := sb.ExpiresAt.Add(time.Duration(req.AdditionalSeconds) * time.Second)
	if newExpiry.After(softCap) {
		newExpiry = softCap
	}
	if !newExpiry.After(sb.ExpiresAt) {
		writeJSON(w, http.StatusOK, map[string]any{
			"sandbox":     s.sandboxJSON(sb),
			"extended_by": int64(0),
			"at_cap":      true,
		})
		return
	}

	if err := s.store.UpdateSandbox(sb.ID, map[string]any{
		"expires_at":       newExpiry,
		"lifetime_seconds": int64(newExpiry.Sub(sb.CreatedAt).Seconds()),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not extend lifetime")
		return
	}
	sb.ExpiresAt = newExpiry
	sb.LifetimeSeconds = int64(newExpiry.Sub(sb.CreatedAt).Seconds())

	s.logger.Info("lifetime extended", slog.String("sandbox", sb.ID),
		slog.Time("expires_at", newExpiry))
	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox":     s.sandboxJSON(sb),
		"extended_by": req.AdditionalSeconds,
		"at_cap":      newExpiry.Equal(softCap),
	})
}

// handleDestroySandbox is the manual, irreversible teardown. It reuses the
// same teardown code as the expiry reaper but records the reason as
// user_requested. Kept distinct from the automatic path in the UI.
//
// Idempotency: Repeated destroy calls on an already destroyed sandbox return
// 200 OK with the tombstone rather than a 409 error. In-flight builds/deploys
// are cancelled immediately.
func (s *Server) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireOwnedSandbox(w, r)
	if !ok {
		return
	}
	// Idempotency: If already destroyed, return success with current tombstone
	if sb.DestroyedAt != nil || sb.Status == db.StatusDeleted {
		writeJSON(w, http.StatusOK, s.sandboxJSON(sb))
		return
	}
	// Abort any active build or deployment in flight
	if s.deployer != nil {
		s.deployer.CancelDeploy(sb.ID)
	}
	if err := s.destroyPermentally(r.Context(), sb, db.DestroyUserRequested); err != nil {
		s.logger.Warn("manual destroy failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not destroy sandbox")
		return
	}
	// destroyPermentally updates the row, not the in-memory struct. Re-read it
	// so the response cannot claim a tombstone is still running with a container
	// that was just removed.
	destroyed, err := s.store.GetSandbox(sb.ID)
	if err != nil {
		s.logger.Warn("manual destroy reload failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "sandbox destroyed but could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxJSON(destroyed))
}

// handleRedeploy triggers a manual rebuild (used after edits are saved and by
// the "redeploy" button). Runs in the background.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}
	if sb.DestroyedAt != nil {
		writeErr(w, http.StatusConflict, "destroyed sandboxes cannot be redeployed; recreate instead")
		return
	}
	opts := orchestrator.DeployOptions{Trigger: db.TriggerManual}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), orchestrator.DeployTimeout)
		defer cancel()
		if err := s.deployer.DeploySandbox(ctx, sb, opts); err != nil {
			s.logger.Warn("manual redeploy failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"redeploying": true})
}

func (s *Server) handleUpdateBranch(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Branch) == "" {
		writeErr(w, http.StatusBadRequest, "branch is required")
		return
	}
	targetBranch := strings.TrimSpace(req.Branch)
	if err := s.store.UpdateSandbox(sb.ID, map[string]any{"branch_name": targetBranch}); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to update branch")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"branch":  targetBranch,
	})
}

// handleSandboxWarnings returns the lifetime-warning state for a sandbox so
// the frontend can render banners on list and detail views (section 2a req 3).
func (s *Server) handleSandboxWarnings(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}
	thresholds, err := s.store.ListWarningThresholds(sb.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read warnings")
		return
	}
	remaining := time.Until(sb.ExpiresAt)

	warnings := make([]map[string]any, 0, len(thresholds))
	for _, t := range thresholds {
		warnings = append(warnings, map[string]any{
			"threshold": t,
			"title":     warningTitle(t),
			"message":   warningMessage(t, remaining),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox_id":        sb.ID,
		"expires_at":        sb.ExpiresAt,
		"remaining_seconds": int64(remaining / time.Second),
		"warnings":          warnings,
	})
}

// statsRange is one selectable window in the metrics history view. The row
// limit is part of the definition, not a global constant: a 7d window at the
// 10s flush cadence is ~60k rows, and the chart cannot draw that many points
// anyway, so each window is capped at a density the chart stays readable at.
type statsRange struct {
	window time.Duration
	limit  int
}

// statsRanges is the allowlist for GET /api/sandboxes/{id}/stats/history?range=.
// Arbitrary durations are rejected rather than parsed: an unvalidated window
// becomes an unbounded scan of a table that only ever grows, which is a cheap
// way for one request to hurt every other sandbox.
var statsRanges = map[string]statsRange{
	"1h":  {window: time.Hour, limit: 360},
	"6h":  {window: 6 * time.Hour, limit: 720},
	"24h": {window: 24 * time.Hour, limit: 1000},
	"7d":  {window: 7 * 24 * time.Hour, limit: 1400},
}

// defaultStatsRange is what an absent ?range= resolves to, matching the
// dashboard's initial "last hour" selection.
const defaultStatsRange = "1h"

// handleStatsHistory serves persisted resource snapshots for the metrics
// chart's non-live ranges. Unlike the WebSocket feed it is a plain REST call:
// history is a fixed set of rows, so there is nothing to stream.
func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireOwnedSandbox(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("range")
	if name == "" {
		name = defaultStatsRange
	}
	span, ok := statsRanges[name]
	if !ok {
		writeErr(w, http.StatusBadRequest, "range must be one of 1h, 6h, 24h, 7d")
		return
	}

	now := time.Now()
	rows, err := s.store.QueryResourceSnapshots(sb.ID, now.Add(-span.window), now, span.limit)
	if err != nil {
		s.logger.Warn("stats history query failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not query resource history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":     name,
		"since":     now.Add(-span.window),
		"until":     now,
		"snapshots": rows,
	})
}

// removeSandboxWorkDir deletes the working directory for a subdomain after
// validating the path stays within the configured work root. The validation
// itself lives in orchestrator.RemoveSandboxWorkDir so every deleter (this
// handler, the expiry reaper, deploy-race cleanup) shares one implementation.
func removeSandboxWorkDir(workRoot, subdomain string) error {
	return orchestrator.RemoveSandboxWorkDir(workRoot, subdomain)
}

// destroyPermentally tears a sandbox down and records its tombstone. The typo
// in the name is intentional documentation: this is permanent and unforgettable.
func (s *Server) destroyPermentally(ctx context.Context, sb *db.Sandbox, reason db.DestructionReason) error {
	// Cancel any active build or deployment in flight
	if s.deployer != nil {
		s.deployer.CancelDeploy(sb.ID)
	}
	if err := s.orch.TeardownSandbox(ctx, sb); err != nil {
		return fmt.Errorf("teardown: %w", err)
	}
	// Remove the on-disk working copy too, mirroring expiry cleanup. Validate
	// the path stays within the configured sandbox work root to avoid deleting
	// the root or another sandbox's files.
	if sb.Subdomain != "" {
		if err := removeSandboxWorkDir(s.cfg.SandboxWorkDir, sb.Subdomain); err != nil {
			s.logger.Warn("manual destroy: removing work dir failed",
				slog.String("sandbox", sb.ID),
				slog.String("subdomain", sb.Subdomain),
				slog.String("err", err.Error()))
			return fmt.Errorf("teardown: removing work dir: %w", err)
		}
	}
	now := time.Now()
	if err := s.store.UpdateSandbox(sb.ID, map[string]any{
		"status":             string(db.StatusDeleted),
		"destroyed_at":       now,
		"destruction_reason": string(reason),
		"container_id":       "",
		"image_tag":          "",
	}); err != nil {
		return err
	}
	// Tombstone is committed: sweep once more for containers a racing deploy
	// created between the teardown pass above and this commit. Best-effort —
	// a sweep failure is logged because the destroy itself already succeeded,
	// and any container created after the commit makes its own deploy fail on
	// ErrSandboxDestroyed and tear down (see SweepSandboxContainers).
	if err := s.orch.SweepSandboxContainers(ctx, sb.ID); err != nil {
		s.logger.Warn("destroy: post-tombstone container sweep failed",
			slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
	}
	return nil
}

// SandboxLogsResponse conveys state-aware logs and lifecycle descriptions.
type SandboxLogsResponse struct {
	Status    string   `json:"status"`
	Available bool     `json:"available"`
	Message   string   `json:"message"`
	Output    string   `json:"output,omitempty"`
	Lines     []string `json:"lines,omitempty"`
}

// handleGetSandboxLogs provides a state-aware REST endpoint for container output.
func (s *Server) handleGetSandboxLogs(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}

	if sb.DestroyedAt != nil || sb.Status == db.StatusDeleted {
		writeJSON(w, http.StatusOK, SandboxLogsResponse{
			Status:    "DESTROYED",
			Available: false,
			Message:   "This sandbox has been destroyed and torn down.",
		})
		return
	}

	if sb.Status == db.StatusFailed {
		writeJSON(w, http.StatusOK, SandboxLogsResponse{
			Status:    "FAILED",
			Available: false,
			Message:   "Container failed during image build and is not running. Please inspect the Build Logs panel.",
		})
		return
	}

	if sb.Status == db.StatusBuilding || sb.Status == db.StatusQueued {
		writeJSON(w, http.StatusOK, SandboxLogsResponse{
			Status:    "BUILDING",
			Available: false,
			Message:   "Container image is currently being built. Application output will appear once the container starts.",
		})
		return
	}

	if sb.Status == db.StatusHibernated {
		writeJSON(w, http.StatusOK, SandboxLogsResponse{
			Status:    "STOPPED",
			Available: false,
			Message:   "Container is hibernated due to inactivity. Click Restart to wake it.",
		})
		return
	}

	if sb.Status == db.StatusRunning {
		if sb.ContainerID == "" {
			writeJSON(w, http.StatusOK, SandboxLogsResponse{
				Status:    "STARTING",
				Available: false,
				Message:   "Waiting for application container to start...",
			})
			return
		}

		output, err := s.orch.GetRecentContainerLogs(r.Context(), sb.ContainerID, 150)
		if err != nil {
			writeJSON(w, http.StatusOK, SandboxLogsResponse{
				Status:    "HEALTHCHECK_PENDING",
				Available: false,
				Message:   "Container is starting; waiting for application readiness...",
			})
			return
		}

		rawLines := strings.Split(strings.TrimSpace(output), "\n")
		var cleanLines []string
		for _, l := range rawLines {
			if trimmed := strings.TrimRight(l, "\r"); trimmed != "" {
				cleanLines = append(cleanLines, trimmed)
			}
		}

		writeJSON(w, http.StatusOK, SandboxLogsResponse{
			Status:    "RUNNING",
			Available: true,
			Message:   "Application is running.",
			Output:    output,
			Lines:     cleanLines,
		})
		return
	}

	writeJSON(w, http.StatusOK, SandboxLogsResponse{
		Status:    string(sb.Status),
		Available: false,
		Message:   fmt.Sprintf("Sandbox is in %s state.", sb.Status),
	})
}

// handleReportActivity allows authorized project members to bump activity
// (e.g., clicking "I'm still here", interaction in in-browser IDE or preview).
func (s *Server) handleReportActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}
	if sb.DestroyedAt != nil || sb.Status == db.StatusExpired || sb.Status == db.StatusDeleted {
		writeErr(w, http.StatusConflict, "sandbox has expired or is deleted")
		return
	}
	s.touchSandbox(r.Context(), sb, user.ID)
	ttl := s.cache.GetIdleTTL(r.Context(), sb.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                     true,
		"status":                 sb.Status,
		"user_id":                user.ID,
		"idle_ttl_remaining_sec": int64(ttl.Seconds()),
		"in_hibernation_warning": s.cache.HasHibernationWarning(r.Context(), sb.ID),
		"in_grace_period":        s.cache.InGracePeriod(r.Context(), sb.ID),
	})
}

// handleLifecycleStatus provides real-time lifecycle tracking across all members.
func (s *Server) handleLifecycleStatus(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}
	ttl := s.cache.GetIdleTTL(r.Context(), sb.ID)
	activeOp, hasOp := s.cache.GetActiveOperation(r.Context(), sb.ID)
	inWarning := s.cache.HasHibernationWarning(r.Context(), sb.ID)
	inGrace := s.cache.InGracePeriod(r.Context(), sb.ID)
	hasUserActivity := s.cache.HasActiveUserActivity(r.Context(), sb.ID)

	retentionDur := time.Duration(sb.WorkspaceRetentionSeconds) * time.Second
	if retentionDur <= 0 {
		retentionDur = 7 * 24 * time.Hour
	}

	var expirationRemainingSec *int64
	var expiresAtVal *time.Time
	expirationCountdownText := "Not applicable"

	if sb.Status == db.StatusHibernated && !sb.ExpiresAt.IsZero() && sb.ExpiresAt.After(time.Unix(0, 0)) {
		rem := int64(time.Until(sb.ExpiresAt).Seconds())
		expirationRemainingSec = &rem
		expiresAtVal = &sb.ExpiresAt
		if rem <= 0 {
			expirationCountdownText = "Expired"
		} else {
			hours := rem / 3600
			mins := (rem % 3600) / 60
			secs := rem % 60
			expirationCountdownText = fmt.Sprintf("%02d:%02d:%02d", hours, mins, secs)
		}
	} else if sb.Status == db.StatusExpired {
		expirationCountdownText = "Expired"
	}

	var retentionExpiresAt time.Time
	if sb.DestroyedAt != nil {
		retentionExpiresAt = sb.DestroyedAt.Add(retentionDur)
	} else if sb.Status == db.StatusHibernated && expiresAtVal != nil {
		retentionExpiresAt = expiresAtVal.Add(retentionDur)
	} else {
		retentionExpiresAt = time.Now().Add(retentionDur)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox_id":                    sb.ID,
		"status":                        sb.Status,
		"lifecycle_version":             sb.LifecycleVersion,
		"idle_timeout_seconds":          sb.IdleTimeoutSeconds,
		"idle_ttl_remaining_sec":        int64(ttl.Seconds()),
		"has_active_user_activity":      hasUserActivity,
		"in_hibernation_warning":        inWarning,
		"in_grace_period":               inGrace,
		"active_operation":              activeOp,
		"has_active_operation":          hasOp,
		"hibernated_at":                 sb.HibernatedAt,
		"hibernated_expiration_seconds": sb.HibernatedExpirationSeconds,
		"workspace_retention_seconds":   sb.WorkspaceRetentionSeconds,
		"expiration_active":             sb.Status == db.StatusHibernated,
		"expires_at":                    expiresAtVal,
		"expiration_remaining_sec":      expirationRemainingSec,
		"expiration_countdown_text":     expirationCountdownText,
		"workspace_preserved":           true,
		"retention_expires_at":          retentionExpiresAt,
	})
}

type updateSettingsRequest struct {
	IdleTimeoutSeconds          *int    `json:"idleTimeoutSeconds"`
	HibernatedExpirationSeconds *int64  `json:"hibernatedExpirationSeconds"`
	WorkspaceRetentionSeconds   *int64  `json:"workspaceRetentionSeconds"`
	LifetimeSeconds             *int64  `json:"lifetimeSeconds"`
	StartCommand                *string `json:"startCommand"`
	Port                        *int    `json:"port"`
	HealthCheckType             *string `json:"healthCheckType"`
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireOwnedSandbox(w, r)
	if !ok {
		return
	}
	var req updateSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	idleSec := sb.IdleTimeoutSeconds
	if req.IdleTimeoutSeconds != nil && *req.IdleTimeoutSeconds > 0 {
		idleSec = *req.IdleTimeoutSeconds
	}

	hibExpSec := sb.HibernatedExpirationSeconds
	if req.HibernatedExpirationSeconds != nil && *req.HibernatedExpirationSeconds > 0 {
		hibExpSec = *req.HibernatedExpirationSeconds
	}

	retentionSec := sb.WorkspaceRetentionSeconds
	if req.WorkspaceRetentionSeconds != nil && *req.WorkspaceRetentionSeconds > 0 {
		retentionSec = *req.WorkspaceRetentionSeconds
	}

	if err := s.store.UpdateLifecycleConfig(sb.ID, idleSec, hibExpSec, retentionSec); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update lifecycle config")
		return
	}

	updated, _ := s.store.GetSandbox(sb.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "Lifecycle settings updated successfully",
		"sandbox": s.sandboxJSON(updated),
	})
}

type startOperationRequest struct {
	Type               string `json:"type"`
	MaxDurationSeconds int64  `json:"max_duration_seconds"`
}

func (s *Server) handleStartOperation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}
	if sb.DestroyedAt != nil || sb.Status == db.StatusExpired {
		writeErr(w, http.StatusConflict, "sandbox has expired")
		return
	}
	var req startOperationRequest
	if err := decodeJSON(r, &req); err != nil || req.Type == "" {
		writeErr(w, http.StatusBadRequest, "operation type is required")
		return
	}
	dur := time.Duration(req.MaxDurationSeconds) * time.Second
	if dur <= 0 || dur > 30*time.Minute {
		dur = 15 * time.Minute // bounded 15-minute max
	}
	if err := s.cache.StartOperation(r.Context(), sb.ID, req.Type, user.ID, dur); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("lifecycle: active operation started",
		slog.String("sandbox", sb.ID),
		slog.String("op_type", req.Type),
		slog.String("user_id", user.ID))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"operation":            req.Type,
		"max_duration_seconds": int64(dur.Seconds()),
	})
}

func (s *Server) handleEndOperation(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}
	_ = s.cache.EndOperation(r.Context(), sb.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRestoreExpiredSandbox creates a NEW sandbox from the preserved workspace
// files of an expired sandbox. The expired runtime is NEVER revived.
func (s *Server) handleRestoreExpiredSandbox(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireOwnedSandbox(w, r)
	if !ok {
		return
	}
	if sb.Status != db.StatusExpired && sb.Status != db.StatusDeleted && sb.DestroyedAt == nil {
		writeErr(w, http.StatusBadRequest, "only expired sandboxes can be restored to a new instance")
		return
	}
	srcDir := filepath.Join(s.cfg.SandboxWorkDir, sb.Subdomain)
	if _, err := os.Stat(srcDir); err != nil {
		writeErr(w, http.StatusGone, "preserved workspace data has exceeded retention and is no longer available")
		return
	}

	now := time.Now().UTC()
	newID := uuid.NewString()
	newSubdomain := fmt.Sprintf("%s-restored-%s", sb.Subdomain, newID[:6])
	if len(newSubdomain) > 50 {
		newSubdomain = newSubdomain[:50]
	}

	newSb := &db.Sandbox{
		ID:                 newID,
		RepositoryID:       sb.RepositoryID,
		ParentSandboxID:    &sb.ID,
		BranchName:         sb.BranchName,
		Subdomain:          newSubdomain,
		Status:             db.StatusQueued,
		IdleTimeoutSeconds: sb.IdleTimeoutSeconds,
		LifetimeSeconds:    sb.LifetimeSeconds,
		ExpiresAt:          now.Add(time.Duration(sb.LifetimeSeconds) * time.Second),
		CreatedAt:          now,
		BuildConfig:        sb.BuildConfig,
	}

	if err := s.store.CreateSandbox(newSb); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create restored sandbox")
		return
	}

	destDir := filepath.Join(s.cfg.SandboxWorkDir, newSubdomain)
	_ = copyDir(srcDir, destDir)

	s.logger.Info("lifecycle: created new sandbox instance from preserved workspace",
		slog.String("expired_sandbox", sb.ID),
		slog.String("new_sandbox", newID),
		slog.String("user_id", user.ID))

	writeJSON(w, http.StatusCreated, map[string]any{
		"message":     "New sandbox instance created from preserved workspace data",
		"sandbox":     s.sandboxJSON(newSb),
		"original_id": sb.ID,
		"new_id":      newID,
	})
}
