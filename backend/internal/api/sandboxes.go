package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/git"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Lifetime constants shared with the frontend's picker.
const (
	// MaxLifetimeSeconds caps the planned lifetime (default 7 days, overridable
	// via MAX_LIFETIME_SECONDS).
	MaxLifetimeSeconds = 7 * 24 * 3600
	// MinLifetimeSeconds keeps a sandbox alive long enough to be useful: the
	// lifetime clock starts at first activation, so anything shorter would be
	// spent largely on the build itself. One hour is the floor.
	MinLifetimeSeconds = 60 * 60
)

// createSandboxRequest is the body of POST /api/sandboxes.
type createSandboxRequest struct {
	RepoURL            string                        `json:"repo_url"`
	Branch             string                        `json:"branch"`
	LifetimeSeconds    int64                         `json:"lifetime_seconds"`
	IdleTimeoutSeconds int                           `json:"idle_timeout_seconds"`
	Database           string                        `json:"database"` // auto | postgres | mysql | none
	EnvVars            map[string]string             `json:"env_vars"`
	BuildConfig        *orchestrator.UserBuildConfig `json:"build_config"`
}

// handleListSandboxes returns the signed-in user's sandboxes, newest first.
// Expired sandboxes still appear (as tombstone rows) so the user can recreate.
func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	rows, err := s.store.ListSandboxes(user.ID)
	if err != nil {
		s.logger.Warn("list sandboxes failed", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not list sandboxes")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, sb := range rows {
		out = append(out, s.sandboxJSON(sb))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": out})
}

// handleCreateSandbox accepts a repo URL plus lifetime/idle-timeout controls,
// creates the sandbox row, and kicks off the deploy pipeline in the
// background. The response is 202 with the queued sandbox.
func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var req createSandboxRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RepoURL == "" {
		writeErr(w, http.StatusBadRequest, "repo_url is required")
		return
	}
	info, err := git.ParseGitHubURL(req.RepoURL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Lifetime validation — this control permanently destroys data, so the
	// backend enforces the bounds and the frontend must show it prominently.
	lifetime := req.LifetimeSeconds
	if lifetime == 0 {
		lifetime = 24 * 3600 // schema default
	}
	if lifetime < MinLifetimeSeconds || lifetime > s.maxLifetime() {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("lifetime_seconds must be between %d and %d", MinLifetimeSeconds, s.maxLifetime()))
		return
	}
	idleTimeout := req.IdleTimeoutSeconds
	if idleTimeout < 60 {
		idleTimeout = 900 // schema default
	}

	branch := req.Branch
	if branch == "" {
		branch = info.DefaultBranch
	}

	repo, err := s.store.FindOrCreateRepository(user.ID, info.CloneURL, info.DefaultBranch)
	if err != nil {
		s.logger.Warn("repository lookup failed", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not store repository")
		return
	}

	subdomain, err := s.uniqueSubdomain(info.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not allocate subdomain")
		return
	}

	now := time.Now()
	var initialBuildConfig datatypes.JSON
	if req.BuildConfig != nil {
		if b, err := json.Marshal(req.BuildConfig); err == nil {
			initialBuildConfig = datatypes.JSON(b)
		}
	}
	sandbox := &db.Sandbox{
		ID:                 uuid.NewString(),
		RepositoryID:       repo.ID,
		BranchName:         branch,
		Subdomain:          subdomain,
		Status:             db.StatusQueued,
		IdleTimeoutSeconds: idleTimeout,
		LifetimeSeconds:    lifetime,
		ExpiresAt:          now.Add(time.Duration(lifetime) * time.Second),
		CreatedAt:          now,
		BuildConfig:        initialBuildConfig,
	}
	if err := s.store.CreateSandboxWithinLimits(sandbox, user.ID, s.maxActiveSandboxes(), s.maxRunningSandboxes()); err != nil {
		if errors.Is(err, db.ErrSandboxQuotaExceeded) {
			s.writeSandboxQuotaExceeded(w)
			return
		}
		if errors.Is(err, db.ErrGlobalCapacityReached) {
			s.writeGlobalCapacityExceeded(w)
			return
		}
		s.logger.Warn("sandbox create failed", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not create sandbox")
		return
	}

	// Persist any user-supplied env vars (encrypted immediately).
	for key, value := range req.EnvVars {
		enc, err := s.box.Encrypt([]byte(value))
		if err != nil {
			continue
		}
		_ = s.store.UpsertEnvVar(sandbox.ID, key, enc, false)
	}

	// Deploy asynchronously; the client polls the sandbox until running.
	opts := orchestrator.DeployOptions{
		Trigger:          db.TriggerInitial,
		DatabaseOverride: req.Database,
		UserBuildConfig:  req.BuildConfig,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), orchestrator.DeployTimeout)
		defer cancel()
		if err := s.deployer.DeploySandbox(ctx, sandbox, opts); err != nil {
			s.logger.Warn("deploy failed", slog.String("sandbox", sandbox.ID), slog.String("err", err.Error()))
		}
	}()

	writeJSON(w, http.StatusAccepted, s.sandboxJSON(sandbox))
}

// handleGetSandbox returns one sandbox's current state.
func (s *Server) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireOwnedSandbox(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxJSON(sb))
}

// --- helpers -------------------------------------------------------------

// errSandboxForbidden marks a failed sandbox access check. It is deliberately
// uninformative: "not yours" and "does not exist" are the same answer, so the
// API never confirms that someone else's sandbox ID is real.
var errSandboxForbidden = errors.New("sandbox not accessible")

// authorizeSandboxAccess resolves the {id} path param and confirms the
// signed-in user owns it. Every per-sandbox route funnels through here.
//
// It deliberately does not touch the ResponseWriter. The live WebSocket
// endpoints need to fail *after* the handshake with a close code, by which
// point an HTTP status can no longer be sent; requireOwnedSandbox is the
// writing wrapper used by the REST handlers.
func (s *Server) authorizeSandboxAccess(r *http.Request) (*db.Sandbox, error) {
	return s.authorizeSandboxAccessWithRole(r, db.ProjectMemberRoleViewer)
}

// requireSandboxRole verifies the requester has at least minRole for the sandbox.
func (s *Server) requireSandboxRole(w http.ResponseWriter, r *http.Request, minRole db.ProjectMemberRole) (*db.Sandbox, bool) {
	if _, ok := s.userFrom(r); !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	sb, err := s.authorizeSandboxAccessWithRole(r, minRole)
	if err != nil {
		if errors.Is(err, errSandboxForbidden) || strings.Contains(err.Error(), "forbidden") {
			writeErr(w, http.StatusForbidden, err.Error())
		} else {
			writeErr(w, http.StatusNotFound, "sandbox not found")
		}
		return nil, false
	}
	return sb, true
}

// requireOwnedSandbox is the REST-shaped wrapper requiring OWNER role.
func (s *Server) requireOwnedSandbox(w http.ResponseWriter, r *http.Request) (*db.Sandbox, bool) {
	return s.requireSandboxRole(w, r, db.ProjectMemberRoleOwner)
}

// requireSandboxRoleForMutation enforces that the requester has EDITOR or OWNER permissions,
// and enforces backend Copy-On-Write: if an EDITOR attempts to modify a sandbox they do not directly own,
// the original sandbox is preserved unchanged, and a fork is automatically created and associated
// with the editor before the modification proceeds.
func (s *Server) requireSandboxRoleForMutation(w http.ResponseWriter, r *http.Request) (*db.Sandbox, bool) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}

	id := chi.URLParam(r, "id")
	// 1. Direct ownership: owner modifies their own sandbox directly
	if owns, err := s.store.OwnsSandbox(id, user.ID); err == nil && owns {
		sb, err := s.store.GetSandbox(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "sandbox not found")
			return nil, false
		}
		return sb, true
	}

	// 2. Otherwise verify caller has at least EDITOR role in the project
	role, err := s.store.GetSandboxRole(id, user.ID)
	if err != nil || (role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor) {
		writeErr(w, http.StatusForbidden, "forbidden: viewers cannot modify sandboxes")
		return nil, false
	}

	// 3. The caller is an EDITOR modifying a shared sandbox they do not directly own.
	// Backend-enforced Copy-On-Write (Non-negotiable requirement #7):
	// The original sandbox MUST remain unchanged. A fork must be created and
	// associated with the editor before modifications are allowed.
	parent, err := s.store.GetSandbox(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "parent sandbox not found")
		return nil, false
	}

	child, err := s.createCopyOnWriteFork(r.Context(), parent, user)
	if err != nil {
		s.logger.Warn("copy-on-write fork failed", slog.String("parent", parent.ID), slog.String("user", user.ID), slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not provision copy-on-write sandbox fork: "+err.Error())
		return nil, false
	}

	w.Header().Set("X-CoW-Forked-Sandbox-ID", child.ID)
	w.Header().Set("X-CoW-Forked-Subdomain", child.Subdomain)
	return child, true
}

func (s *Server) createCopyOnWriteFork(ctx context.Context, parent *db.Sandbox, user *db.User) (*db.Sandbox, error) {
	subdomain := "cow-" + parent.Subdomain
	var err error
	if s.store != nil {
		subdomain, err = s.uniqueSubdomain(subdomain)
		if err != nil {
			return nil, fmt.Errorf("allocating subdomain: %w", err)
		}
	}

	remaining := time.Until(parent.ExpiresAt)
	childLifetime := int64(remaining / time.Second)
	if childLifetime < MinLifetimeSeconds {
		childLifetime = MinLifetimeSeconds
	}
	if childLifetime > s.maxLifetime() {
		childLifetime = s.maxLifetime()
	}

	now := time.Now().UTC()
	child := &db.Sandbox{
		ID:                 uuid.NewString(),
		RepositoryID:       parent.RepositoryID,
		ParentSandboxID:    &parent.ID,
		BranchName:         parent.BranchName,
		Subdomain:          subdomain,
		Status:             db.StatusRunning,
		IdleTimeoutSeconds: parent.IdleTimeoutSeconds,
		LifetimeSeconds:    childLifetime,
		ExpiresAt:          now.Add(time.Duration(childLifetime) * time.Second),
		CreatedAt:          now,
	}

	if s.store != nil {
		if err := s.store.CreateSandboxWithinLimits(child, user.ID, s.maxActiveSandboxes(), s.maxRunningSandboxes()); err != nil {
			return nil, fmt.Errorf("creating sandbox within limits: %w", err)
		}
		if projectID, pErr := s.store.GetProjectIDForSandbox(parent.ID); pErr == nil && projectID != "" {
			_ = s.store.AssociateProjectSandbox(projectID, child.ID)
		}
		_ = s.store.RecordAuditEvent(&db.AuditEvent{
			ID:        uuid.NewString(),
			UserID:    &user.ID,
			Action:    "SANDBOX_COW_FORK",
			Resource:  fmt.Sprintf("parent:%s -> child:%s", parent.ID, child.ID),
			CreatedAt: now,
		})
	}

	// Deep-copy parent workspace directory to child workspace directory
	if s.cfg != nil && s.cfg.SandboxWorkDir != "" {
		parentDir := filepath.Join(s.cfg.SandboxWorkDir, parent.Subdomain)
		childDir := filepath.Join(s.cfg.SandboxWorkDir, child.Subdomain)
		if info, err := os.Stat(parentDir); err == nil && info.IsDir() {
			_ = copyDir(parentDir, childDir)
		}
	}

	return child, nil
}

// sandboxJSON renders a sandbox row plus computed fields the UI needs.
func (s *Server) sandboxJSON(sb *db.Sandbox) map[string]any {
	var dbEngine string
	if s.store != nil {
		if dbs, err := s.store.ListSandboxDatabases(sb.ID); err == nil && len(dbs) > 0 {
			dbEngine = dbs[0].Engine
		}
	}

	sandboxURL := "http://" + sb.Subdomain + "." + s.cfg.SandboxBaseDomain
	if s.cfg.GatewayPort != 80 && s.cfg.GatewayPort != 443 && s.cfg.GatewayPort > 0 {
		sandboxURL += fmt.Sprintf(":%d", s.cfg.GatewayPort)
	}

	return map[string]any{
		"id":                            sb.ID,
		"subdomain":                     sb.Subdomain,
		"url":                           sandboxURL,
		"status":                        sb.Status,
		"branch":                        sb.BranchName,
		"githubBranch":                  sb.BranchName,
		"parent_sandbox_id":             sb.ParentSandboxID,
		"container_id":                  sb.ContainerID,
		"internal_port":                 sb.InternalPort,
		"database_engine":               dbEngine,
		"idle_timeout_seconds":          sb.IdleTimeoutSeconds,
		"last_request_at":               sb.LastRequestAt,
		"lifetime_seconds":              sb.LifetimeSeconds,
		"expires_at":                    sb.ExpiresAt,
		"hibernated_at":                 sb.HibernatedAt,
		"hibernated_expiration_seconds": sb.HibernatedExpirationSeconds,
		"workspace_retention_seconds":   sb.WorkspaceRetentionSeconds,
		"lifecycle_version":             sb.LifecycleVersion,
		// Null until the sandbox first goes live; the client uses it to show that
		// the lifetime clock has not started yet.
		"activated_at":       sb.ActivatedAt,
		"destroyed_at":       sb.DestroyedAt,
		"destruction_reason": sb.DestructionReason,
		"detected_language":  sb.DetectedLanguage,
		"build_config":       sb.BuildConfig,
		"created_at":         sb.CreatedAt,
		"updated_at":         sb.UpdatedAt,
	}
}

// uniqueSubdomain builds a routable subdomain: slugified repo name plus a
// short random suffix, retried against collisions.
func (s *Server) uniqueSubdomain(repoName string) (string, error) {
	base := slugify(repoName)
	if base == "" {
		base = "sandbox"
	}
	for i := 0; i < 5; i++ {
		suffix := randomHex(3) // 6 hex chars
		candidate := base + "-" + suffix
		if _, err := s.store.GetSandboxBySubdomain(candidate); err != nil {
			return candidate, nil // no collision found
		}
	}
	return "", fmt.Errorf("could not find a free subdomain")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify lowercases and collapses the repo name to a DNS-safe label.
func slugify(name string) string {
	name = strings.ToLower(name)
	name = nonSlug.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return strings.TrimSuffix(name, "-")
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)
}

func (s *Server) maxLifetime() int64 {
	if m := s.cfg.MaxLifetimeSeconds; m > 0 {
		return m
	}
	return MaxLifetimeSeconds
}

// decodeJSON reads and closes the request body into v.
func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// warningTitle returns the short heading for a sent warning tier.
func warningTitle(threshold string) string {
	switch threshold {
	case "24h":
		return "Less than 24 hours left"
	case "1h":
		return "Less than 1 hour left"
	case "5m":
		return "Less than 5 minutes left"
	case "p50":
		return "Half the lifetime is gone"
	case "p25":
		return "Three quarters of the lifetime is gone"
	default:
		return "Lifetime warning"
	}
}

// warningMessage renders the plain-language warning copy (section 2a reuse
// of the idle-vs-lifetime distinction where useful).
func warningMessage(threshold string, remaining time.Duration) string {
	h := int(remaining.Hours())
	m := int(remaining.Minutes()) % 60
	switch threshold {
	case "p50", "p25":
		return "This sandbox will be permanently deleted soon. Everything is destroyed at expiry. Extend the lifetime if you still need it."
	default:
		if h > 0 {
			return fmt.Sprintf("This sandbox will be permanently deleted in about %dh %dm. Data is lost at that point, for good. Extend if you still need it.", h, m)
		}
		return fmt.Sprintf("This sandbox will be permanently deleted in about %dm. There is no undo — extend it now if you still need it.", m)
	}
}
