package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store bundles the gorm handle plus every query. Keeping all SQL-shaped
// operations here (rather than scattered in handlers) makes the access layer
// reviewable and testable in one place.
type Store struct {
	db *gorm.DB
}

// NewStore returns a Store wrapping the given gorm handle.
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Ping verifies database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("db: store not initialized")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("db: accessing underlying sql.DB: %w", err)
	}
	return sqlDB.PingContext(ctx)
}

func newID() string {
	return uuid.NewString()
}

// ---- users --------------------------------------------------------------

// UpsertUser inserts the user if new, or updates profile fields and the
// (rotated) GitHub token if the user already exists. GitHub identity is the
// unique key, per the schema.
func (s *Store) UpsertUser(u *User) error {
	if u.ID == "" {
		u.ID = newID()
	}
	existing := &User{}
	err := s.db.Where("github_id = ?", u.GithubID).First(existing).Error
	if err == gorm.ErrRecordNotFound {
		return s.db.Create(u).Error
	}
	if err != nil {
		return fmt.Errorf("db: looking up user: %w", err)
	}
	// Preserve the stable UUID; refresh everything else.
	u.ID = existing.ID
	u.CreatedAt = existing.CreatedAt
	return s.db.Save(u).Error
}

// GetUserByGithubID fetches a user by their GitHub numeric identity.
func (s *Store) GetUserByGithubID(id int64) (*User, error) {
	u := &User{}
	if err := s.db.Where("github_id = ?", id).First(u).Error; err != nil {
		return nil, fmt.Errorf("db: fetching user %d: %w", id, err)
	}
	return u, nil
}

// GetUser fetches a user by internal UUID.
func (s *Store) GetUser(id string) (*User, error) {
	u := &User{}
	if err := s.db.First(u, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("db: fetching user %s: %w", id, err)
	}
	return u, nil
}

// SearchUsers returns registered users whose username or email matches query.
func (s *Store) SearchUsers(query string, limit int) ([]*User, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	searchPattern := "%" + query + "%"
	var users []*User
	if err := s.db.Where("email ILIKE ? OR username ILIKE ?", searchPattern, searchPattern).Limit(limit).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("db: searching users: %w", err)
	}
	return users, nil
}

// ---- repositories -------------------------------------------------------

// FindOrCreateRepository returns the user's repository row for githubURL,
// creating it on first use.
func (s *Store) FindOrCreateRepository(userID, githubURL, defaultBranch string) (*Repository, error) {
	repo := &Repository{}
	err := s.db.Where("user_id = ? AND github_url = ?", userID, githubURL).First(repo).Error
	if err == nil {
		return repo, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("db: looking up repository: %w", err)
	}
	repo = &Repository{
		ID:            newID(),
		UserID:        userID,
		GithubURL:     githubURL,
		DefaultBranch: defaultBranch,
	}
	if err := s.db.Create(repo).Error; err != nil {
		return nil, fmt.Errorf("db: creating repository: %w", err)
	}
	return repo, nil
}

// GetRepository fetches a repository by ID.
func (s *Store) GetRepository(id string) (*Repository, error) {
	repo := &Repository{}
	if err := s.db.First(repo, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("db: fetching repository %s: %w", id, err)
	}
	return repo, nil
}

// ---- sandboxes ----------------------------------------------------------

// CreateSandbox persists a new sandbox row sized from the deploy request.
//
// ExpiresAt is anchored at CreatedAt here as a provisional deadline: it is what
// cleans up a sandbox whose build never finishes. The real clock starts when the
// sandbox first goes live, via MarkSandboxActive.
func (s *Store) CreateSandbox(sb *Sandbox) error {
	if sb.ID == "" {
		sb.ID = newID()
	}
	sb.ExpiresAt = sb.CreatedAt.Add(time.Duration(sb.LifetimeSeconds) * time.Second)
	return s.db.Create(sb).Error
}

// ErrSandboxQuotaExceeded is returned by CreateSandboxWithinQuota when the
// owning user already has the maximum number of live sandboxes.
var ErrSandboxQuotaExceeded = errors.New("db: user's active sandbox quota is exhausted")

// ErrGlobalCapacityReached is returned when the platform-wide live-sandbox
// cap is already full. It is distinct from a user's quota so the API can tell
// a user to wait for somebody else's sandbox to expire instead of suggesting
// they destroy one of their own.
var ErrGlobalCapacityReached = errors.New("db: platform sandbox capacity is exhausted")

// CreateSandboxWithinQuota creates a sandbox row only while the owning user
// is below maxActive live sandboxes. It is CreateSandboxWithinLimits with no
// platform-wide cap.
func (s *Store) CreateSandboxWithinQuota(sb *Sandbox, ownerUserID string, maxActive int) error {
	return s.CreateSandboxWithinLimits(sb, ownerUserID, maxActive, 0)
}

// CreateSandboxWithinLimits creates a sandbox row only while the owning user
// is below maxActive live sandboxes and, when maxRunning > 0, the platform is
// below maxRunning live sandboxes in total.
//
// The check-and-insert is atomic: per-user and global Postgres advisory locks
// (transaction-scoped, released automatically on commit or rollback) serialize
// the checks across concurrent requests and backend instances, so racing
// creations can never overshoot either cap. The count uses the same tombstone
// definition as the rest of the platform: sandboxes with destroyed_at set
// (manual destroy or lifetime expiry) drop out immediately.
func (s *Store) CreateSandboxWithinLimits(sb *Sandbox, ownerUserID string, maxActive, maxRunning int) error {
	if sb.ID == "" {
		sb.ID = newID()
	}
	sb.ExpiresAt = sb.CreatedAt.Add(time.Duration(sb.LifetimeSeconds) * time.Second)
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Advisory locks take an arbitrary bigint key; hashing the user ID
		// yields a stable per-user key with a 64-bit keyspace.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
			"sandbox-quota:"+ownerUserID).Error; err != nil {
			return fmt.Errorf("db: acquiring sandbox quota lock: %w", err)
		}
		var active int64
		if err := tx.Raw(`SELECT count(*) FROM sandboxes s
			JOIN repositories r ON r.id = s.repository_id
			WHERE r.user_id = ? AND s.destroyed_at IS NULL`, ownerUserID).Scan(&active).Error; err != nil {
			return fmt.Errorf("db: counting active sandboxes: %w", err)
		}
		if active >= int64(maxActive) {
			return ErrSandboxQuotaExceeded
		}
		if maxRunning > 0 {
			// A single global lock serializes every creator, so the global
			// count cannot race. It is taken after the per-user lock, and all
			// creators use the same order, so there is no deadlock cycle.
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
				"sandbox-global-capacity").Error; err != nil {
				return fmt.Errorf("db: acquiring global sandbox capacity lock: %w", err)
			}
			var running int64
			if err := tx.Raw(`SELECT count(*) FROM sandboxes WHERE destroyed_at IS NULL`).Scan(&running).Error; err != nil {
				return fmt.Errorf("db: counting live sandboxes: %w", err)
			}
			if running >= int64(maxRunning) {
				return ErrGlobalCapacityReached
			}
		}
		if err := tx.Create(sb).Error; err != nil {
			return fmt.Errorf("db: creating sandbox: %w", err)
		}
		return nil
	})
}

// CreateSandboxWithProject creates a sandbox and establishes its authoritative project association
// within a single atomic database transaction.
func (s *Store) CreateSandboxWithProject(sb *Sandbox, ownerUserID, projectID string, maxActive, maxRunning int) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("db: projectID is required for sandbox creation")
	}
	if sb.ID == "" {
		sb.ID = newID()
	}
	sb.ExpiresAt = sb.CreatedAt.Add(time.Duration(sb.LifetimeSeconds) * time.Second)

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Validate destination project exists
		var proj Project
		if err := tx.Where("id = ?", projectID).First(&proj).Error; err != nil {
			return fmt.Errorf("db: destination project not found: %w", err)
		}

		// 2. Acquire quota advisory locks
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
			"sandbox-quota:"+ownerUserID).Error; err != nil {
			return fmt.Errorf("db: acquiring sandbox quota lock: %w", err)
		}

		var active int64
		if err := tx.Raw(`SELECT count(*) FROM sandboxes s
			JOIN repositories r ON r.id = s.repository_id
			WHERE r.user_id = ? AND s.destroyed_at IS NULL`, ownerUserID).Scan(&active).Error; err != nil {
			return fmt.Errorf("db: counting active sandboxes: %w", err)
		}
		if active >= int64(maxActive) {
			return ErrSandboxQuotaExceeded
		}

		if maxRunning > 0 {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
				"sandbox-global-capacity").Error; err != nil {
				return fmt.Errorf("db: acquiring global sandbox capacity lock: %w", err)
			}
			var running int64
			if err := tx.Raw(`SELECT count(*) FROM sandboxes WHERE destroyed_at IS NULL`).Scan(&running).Error; err != nil {
				return fmt.Errorf("db: counting live sandboxes: %w", err)
			}
			if running >= int64(maxRunning) {
				return ErrGlobalCapacityReached
			}
		}

		// 3. Insert sandbox record
		if err := tx.Create(sb).Error; err != nil {
			return fmt.Errorf("db: creating sandbox: %w", err)
		}

		// 4. Insert authoritative project association
		ps := &ProjectSandbox{
			ProjectID: projectID,
			SandboxID: sb.ID,
			CreatedAt: time.Now().UTC(),
		}
		if err := tx.Create(ps).Error; err != nil {
			return fmt.Errorf("db: associating sandbox with project: %w", err)
		}

		return nil
	})
}

// ErrSandboxDestroyed is returned by MarkSandboxActive when the row is already a
// tombstone. A deploy that finishes after the user destroyed (or the reaper
// expired) the sandbox must not resurrect it.
var ErrSandboxDestroyed = errors.New("db: sandbox is already destroyed")

// MarkSandboxActive records a transition into 'running' and, on the first one
// only, starts the lifetime clock.
//
// The distinction matters: a redeploy also walks building -> running, so keying
// off status would hand out a fresh full lifetime on every save-and-redeploy and
// make the sandbox immortal. The `activated_at IS NULL` predicate makes the
// rebasing a single atomic claim on the row, so two concurrent deploys cannot
// both win it.
//
// It returns true when this call performed the first activation, and
// ErrSandboxDestroyed if the row is already torn down. `fields` carries the
// running-state columns (container_id, image_tag, ...); expires_at and
// activated_at are owned by this method and are ignored in `fields`.
func (s *Store) MarkSandboxActive(id string, at time.Time, fields map[string]any) (bool, error) {
	// First activation: claim the clock and rebase the deadline in one statement.
	// lifetime_seconds is read from the row rather than passed in, so the new
	// deadline is derived from the same value the reaper and warnings use.
	first := map[string]any{
		"activated_at": at,
		"expires_at":   gorm.Expr("?::timestamptz + (lifetime_seconds * interval '1 second')", at),
	}
	for k, v := range fields {
		first[k] = v
	}
	res := s.db.Model(&Sandbox{}).
		Where("id = ? AND activated_at IS NULL AND destroyed_at IS NULL", id).
		Updates(first)
	if res.Error != nil {
		return false, fmt.Errorf("db: activating sandbox %s: %w", id, res.Error)
	}
	if res.RowsAffected > 0 {
		return true, nil
	}

	// The claim was lost, which is one of two legitimate reasons.
	var sb Sandbox
	if err := s.db.First(&sb, "id = ?", id).Error; err != nil {
		return false, fmt.Errorf("db: reading sandbox %s after failed activation: %w", id, err)
	}
	if sb.DestroyedAt != nil {
		return false, ErrSandboxDestroyed
	}
	// Already activated: a redeploy. Apply the running state but leave the
	// deadline exactly where it was.
	if err := s.db.Model(&Sandbox{}).
		Where("id = ? AND destroyed_at IS NULL", id).
		Updates(fields).Error; err != nil {
		return false, fmt.Errorf("db: updating active sandbox %s: %w", id, err)
	}
	return false, nil
}

// GetSandbox fetches a sandbox by ID.
func (s *Store) GetSandbox(id string) (*Sandbox, error) {
	sb := &Sandbox{}
	if err := s.db.First(sb, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("db: fetching sandbox %s: %w", id, err)
	}
	return sb, nil
}

// GetSandboxBySubdomain fetches a sandbox by its subdomain or historical alias
// (used by the wake/reverse-proxy gateway to resolve incoming requests).
func (s *Store) GetSandboxBySubdomain(sub string) (*Sandbox, error) {
	sub = strings.TrimSpace(strings.ToLower(sub))
	if sub == "" {
		return nil, errors.New("db: empty subdomain")
	}
	sb := &Sandbox{}
	if err := s.db.First(sb, "subdomain = ?", sub).Error; err == nil {
		return sb, nil
	}
	// Check aliases
	var alias SandboxSubdomainAlias
	if err := s.db.Where("subdomain = ?", sub).First(&alias).Error; err == nil {
		if err := s.db.First(sb, "id = ?", alias.SandboxID).Error; err == nil {
			return sb, nil
		}
	}
	return nil, fmt.Errorf("db: fetching sandbox by subdomain %s: not found", sub)
}

var nonSlugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeSlug converts any user-defined name to a clean, URL-safe slug.
func NormalizeSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = nonSlugRegex.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if len(name) > 40 {
		name = name[:40]
		name = strings.TrimSuffix(name, "-")
	}
	if name == "" {
		name = "sandbox"
	}
	return name
}

// RenameSandbox updates the user-defined display name and canonical routing slug/subdomain.
// The old subdomain is preserved in sandbox_subdomain_aliases so legacy links continue to route.
func (s *Store) RenameSandbox(sandboxID, newName string) (*Sandbox, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	newName = strings.TrimSpace(newName)
	if sandboxID == "" {
		return nil, errors.New("sandbox ID is required")
	}
	if newName == "" {
		return nil, errors.New("sandbox name cannot be empty")
	}
	if len(newName) > 100 {
		return nil, errors.New("sandbox name cannot exceed 100 characters")
	}

	sb, err := s.GetSandbox(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("fetching sandbox %s: %w", sandboxID, err)
	}
	if sb.DestroyedAt != nil {
		return nil, errors.New("cannot rename a destroyed sandbox")
	}

	newSlug := NormalizeSlug(newName)
	if newSlug == "" {
		newSlug = "sandbox"
	}

	// Extract unique short ID suffix or derive from sandbox.ID
	shortID := ""
	if idx := strings.LastIndex(sb.Subdomain, "-"); idx != -1 && len(sb.Subdomain)-idx-1 >= 4 {
		shortID = sb.Subdomain[idx+1:]
	} else if len(sb.ID) >= 6 {
		shortID = strings.ReplaceAll(sb.ID, "-", "")[:6]
	} else {
		shortID = "000000"
	}

	newSubdomain := fmt.Sprintf("%s-%s", newSlug, shortID)

	// If only casing changed or slug/subdomain is identical, simply update name
	if newSubdomain == sb.Subdomain {
		if err := s.db.Model(sb).Update("name", newName).Error; err != nil {
			return nil, fmt.Errorf("updating sandbox name: %w", err)
		}
		sb.Name = newName
		return sb, nil
	}

	// If newSubdomain collides with another sandbox, retry with short random hex
	if existing, err := s.GetSandboxBySubdomain(newSubdomain); err == nil && existing.ID != sb.ID {
		for i := 0; i < 5; i++ {
			candidate := fmt.Sprintf("%s-%s", newSlug, newID()[:6])
			if _, err := s.GetSandboxBySubdomain(candidate); err != nil {
				newSubdomain = candidate
				break
			}
		}
	}

	oldSubdomain := sb.Subdomain

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Record old subdomain as alias if it's not already in aliases
		alias := &SandboxSubdomainAlias{
			ID:        newID(),
			SandboxID: sb.ID,
			Subdomain: oldSubdomain,
			CreatedAt: time.Now().UTC(),
		}
		_ = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(alias).Error

		// 2. Remove newSubdomain from aliases if it previously belonged to this sandbox
		_ = tx.Delete(&SandboxSubdomainAlias{}, "subdomain = ?", newSubdomain).Error

		// 3. Update sandbox record
		now := time.Now().UTC()
		if err := tx.Model(sb).Updates(map[string]any{
			"name":       newName,
			"slug":       newSlug,
			"subdomain":  newSubdomain,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}

		// 4. Record audit event
		detailsJSON, _ := json.Marshal(map[string]any{
			"oldName":      sb.Name,
			"newName":      newName,
			"oldSubdomain": oldSubdomain,
			"newSubdomain": newSubdomain,
		})
		projectID, _ := s.GetProjectIDForSandbox(sb.ID)
		var projIDPtr *string
		if projectID != "" {
			projIDPtr = &projectID
		}
		audit := &AuditEvent{
			ID:        newID(),
			ProjectID: projIDPtr,
			Action:    "RENAME_SANDBOX",
			Resource:  "sandbox:" + sb.ID,
			Details:   datatypes.JSON(detailsJSON),
			CreatedAt: now,
		}
		_ = tx.Create(audit).Error

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("renaming sandbox in db: %w", err)
	}

	sb.Name = newName
	sb.Slug = newSlug
	sb.Subdomain = newSubdomain
	return sb, nil
}

// ListSandboxes returns the user's sandboxes, newest first.
// Authoritative multi-tenant visibility:
// A user can see a sandbox if they have active membership in the project it belongs to.
// Any legacy unassociated sandboxes are visible only to the direct repository owner.
func (s *Store) ListSandboxes(userID string) ([]*Sandbox, error) {
	var out []*Sandbox
	query := s.db.Where(`
		id IN (
			SELECT ps.sandbox_id FROM project_sandboxes ps
			WHERE ps.project_id IN (
				SELECT p.id FROM projects p WHERE p.created_by = ?
				UNION
				SELECT pm.project_id FROM project_members pm WHERE pm.user_id = ? AND pm.status = 'ACTIVE'
			)
		)
		OR (
			id NOT IN (SELECT sandbox_id FROM project_sandboxes)
			AND repository_id IN (SELECT id FROM repositories WHERE user_id = ?)
		)
	`, userID, userID, userID)
	if err := query.Order("created_at DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing sandboxes: %w", err)
	}
	return out, nil
}

// ListWangableSandboxes lists sandboxes that are still alive (not yet
// destroyed) and not in a terminal failed state for the warnings worker.
func (s *Store) ListActiveSandboxes() ([]*Sandbox, error) {
	var out []*Sandbox
	if err := s.db.Where("destroyed_at IS NULL AND status NOT IN ?",
		[]SandboxStatus{StatusFailed, StatusExpired}).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing active sandboxes: %w", err)
	}
	return out, nil
}

// ListDuedExpiredSandboxes finds non-destroyed hibernated sandboxes whose expiration
// deadline has passed. Running sandboxes are never expired by this query.
func (s *Store) ListDuedExpiredSandboxes(now time.Time) ([]*Sandbox, error) {
	var out []*Sandbox
	if err := s.db.Where(
		"destroyed_at IS NULL AND status = ? AND expires_at > ? AND expires_at <= ?",
		StatusHibernated, time.Unix(0, 0), now,
	).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing expired sandboxes: %w", err)
	}
	return out, nil
}

// TransitionToHibernated atomically marks a sandbox hibernated, records hibernated_at,
// calculates a fresh expires_at deadline, and increments lifecycle_version to invalidate
// any prior or concurrent jobs.
func (s *Store) TransitionToHibernated(id string, hibernatedAt, expiresAt time.Time) error {
	now := time.Now().UTC()
	res := s.db.Model(&Sandbox{}).Where("id = ? AND status = ?", id, StatusRunning).Updates(map[string]any{
		"status":            StatusHibernated,
		"hibernated_at":     hibernatedAt,
		"expires_at":        expiresAt,
		"lifecycle_version": gorm.Expr("lifecycle_version + 1"),
		"updated_at":        now,
	})
	if res.Error != nil {
		return fmt.Errorf("db: transition to hibernated: %w", res.Error)
	}
	return nil
}

// TransitionToRunning atomically marks a sandbox running after successful wake-up readiness,
// clears hibernated_at and expires_at, and increments lifecycle_version to cancel any pending expiry jobs.
func (s *Store) TransitionToRunning(id string) error {
	now := time.Now().UTC()
	res := s.db.Model(&Sandbox{}).Where("id = ? AND status IN ?", id, []SandboxStatus{StatusHibernated, StatusQueued, StatusBuilding}).Updates(map[string]any{
		"status":            StatusRunning,
		"hibernated_at":     nil,
		"expires_at":        time.Unix(0, 0), // cleared / inactive
		"lifecycle_version": gorm.Expr("lifecycle_version + 1"),
		"updated_at":        now,
	})
	if res.Error != nil {
		return fmt.Errorf("db: transition to running: %w", res.Error)
	}
	return nil
}

// ExpireHibernatedSandbox atomically transitions a hibernated sandbox to expired with fencing.
// It verifies that lifecycle_version matches and status is still hibernated immediately before deletion.
func (s *Store) ExpireHibernatedSandbox(id string, expectedVersion int64, now time.Time) (bool, error) {
	res := s.db.Model(&Sandbox{}).
		Where("id = ? AND lifecycle_version = ? AND status = ? AND expires_at <= ? AND destroyed_at IS NULL",
			id, expectedVersion, StatusHibernated, now).
		Updates(map[string]any{
			"status":             string(StatusExpired),
			"destroyed_at":       now,
			"destruction_reason": string(DestroyLifetimeExpired),
			"container_id":       "",
			"image_tag":          "",
			"lifecycle_version":  gorm.Expr("lifecycle_version + 1"),
			"updated_at":         now,
		})
	if res.Error != nil {
		return false, fmt.Errorf("db: expiring hibernated sandbox: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// UpdateLifecycleConfig updates idle timeout, hibernated expiration, and workspace retention.
// If the sandbox is currently hibernated, it dynamically adjusts expires_at = hibernated_at + timeout.
func (s *Store) UpdateLifecycleConfig(id string, idleTimeoutSec int, hibernatedExpirySec, retentionSec int64) error {
	sb, err := s.GetSandbox(id)
	if err != nil {
		return err
	}
	patch := map[string]any{
		"idle_timeout_seconds":          idleTimeoutSec,
		"hibernated_expiration_seconds": hibernatedExpirySec,
		"workspace_retention_seconds":   retentionSec,
		"updated_at":                    time.Now().UTC(),
	}
	if sb.Status == StatusHibernated && sb.HibernatedAt != nil && !sb.HibernatedAt.IsZero() {
		newExpiry := sb.HibernatedAt.Add(time.Duration(hibernatedExpirySec) * time.Second)
		patch["expires_at"] = newExpiry
		patch["lifecycle_version"] = gorm.Expr("lifecycle_version + 1")
	}
	return s.db.Model(&Sandbox{}).Where("id = ?", id).Updates(patch).Error
}

// UpdateSandbox applies the non-zero fields of patch to the sandbox row.
func (s *Store) UpdateSandbox(id string, patch map[string]any) error {
	patch["updated_at"] = time.Now()
	if err := s.db.Model(&Sandbox{}).Where("id = ?", id).Updates(patch).Error; err != nil {
		return fmt.Errorf("db: updating sandbox %s: %w", id, err)
	}
	return nil
}

// OwnsSandbox reports whether userID owns the sandbox with the given ID.
func (s *Store) OwnsSandbox(sandboxID, userID string) (bool, error) {
	var count int64
	err := s.db.Model(&Sandbox{}).
		Joins("JOIN repositories ON repositories.id = sandboxes.repository_id").
		Where("sandboxes.id = ? AND repositories.user_id = ?", sandboxID, userID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("db: ownership check: %w", err)
	}
	return count > 0, nil
}

// CanAccessSandbox reports whether userID is authorized to access the sandbox
// (either as direct repository owner or as an active project member).
func (s *Store) CanAccessSandbox(sandboxID, userID string) (bool, error) {
	if sandboxID == "" || userID == "" {
		return false, nil
	}
	owns, err := s.OwnsSandbox(sandboxID, userID)
	if err == nil && owns {
		return true, nil
	}
	// Check project_sandboxes + project_members
	var memberCount int64
	err = s.db.Table("project_sandboxes").
		Joins("JOIN project_members ON project_members.project_id = project_sandboxes.project_id").
		Where("project_sandboxes.sandbox_id = ? AND project_members.user_id = ? AND project_members.status = 'ACTIVE'", sandboxID, userID).
		Count(&memberCount).Error
	if err == nil && memberCount > 0 {
		return true, nil
	}
	return false, nil
}

// ---- deployments --------------------------------------------------------

// CreateDeployment records a build attempt in the history table and returns
// the row with its generated ID.
func (s *Store) CreateDeployment(d *Deployment) (*Deployment, error) {
	if d.ID == "" {
		d.ID = newID()
	}
	if err := s.db.Create(d).Error; err != nil {
		return nil, fmt.Errorf("db: creating deployment: %w", err)
	}
	return d, nil
}

// UpdateDeployment patches a deployment row (commonly status and log excerpt).
func (s *Store) UpdateDeployment(id string, patch map[string]any) error {
	if err := s.db.Model(&Deployment{}).Where("id = ?", id).Updates(patch).Error; err != nil {
		return fmt.Errorf("db: updating deployment %s: %w", id, err)
	}
	return nil
}

// ListDeployments returns deploy history for a sandbox, newest first.
func (s *Store) ListDeployments(sandboxID string) ([]*Deployment, error) {
	var out []*Deployment
	if err := s.db.Where("sandbox_id = ?", sandboxID).Order("started_at DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing deployments: %w", err)
	}
	return out, nil
}

// ---- lifetime warnings --------------------------------------------------

// HasWarningSent reports whether the given threshold warning was already sent.
func (s *Store) HasWarningSent(sandboxID, threshold string) (bool, error) {
	var count int64
	err := s.db.Model(&LifetimeWarning{}).
		Where("sandbox_id = ? AND threshold = ?", sandboxID, threshold).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("db: checking warning: %w", err)
	}
	return count > 0, nil
}

// MarkWarningSent records a threshold warning as delivered.
func (s *Store) MarkWarningSent(sandboxID, threshold string) error {
	w := &LifetimeWarning{ID: newID(), SandboxID: sandboxID, Threshold: threshold}
	return s.db.Create(w).Error
}

// ListWarningThresholds returns the thresholds already sent for a sandbox.
func (s *Store) ListWarningThresholds(sandboxID string) ([]string, error) {
	var out []string
	if err := s.db.Model(&LifetimeWarning{}).
		Where("sandbox_id = ?", sandboxID).
		Pluck("threshold", &out).Error; err != nil {
		return nil, fmt.Errorf("db: listing warnings: %w", err)
	}
	return out, nil
}

// ---- env vars -----------------------------------------------------------

// UpsertEnvVar inserts or replaces one secret env var for a sandbox (values
// stored encrypted; encrypt before calling).
func (s *Store) UpsertEnvVar(sandboxID, key string, value []byte, aiSuggested bool) error {
	v := &EnvVar{}
	now := time.Now()
	err := s.db.Where("sandbox_id = ? AND key = ?", sandboxID, key).First(v).Error
	if err == gorm.ErrRecordNotFound {
		v = &EnvVar{
			ID:            newID(),
			SandboxID:     sandboxID,
			Key:           key,
			Value:         value,
			IsAISuggested: aiSuggested,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := s.db.Create(v).Error; err != nil {
			return fmt.Errorf("db: creating env var: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: looking up env var: %w", err)
	}
	return s.db.Model(v).Where("id = ?", v.ID).
		Updates(map[string]any{"value": value, "is_ai_suggested": aiSuggested, "updated_at": now}).Error
}

// GetEnvVar fetches a single environment variable by sandbox ID and key.
func (s *Store) GetEnvVar(sandboxID, key string) (*EnvVar, error) {
	v := &EnvVar{}
	if err := s.db.Where("sandbox_id = ? AND key = ?", sandboxID, key).First(v).Error; err != nil {
		return nil, err
	}
	return v, nil
}

// DeleteEnvVar removes one environment variable by key.
func (s *Store) DeleteEnvVar(sandboxID, key string) error {
	res := s.db.Where("sandbox_id = ? AND key = ?", sandboxID, key).Delete(&EnvVar{})
	if res.Error != nil {
		return fmt.Errorf("db: deleting env var %s: %w", key, res.Error)
	}
	return nil
}

// DeleteAllEnvVars removes all environment variables for a sandbox (used during teardown).
func (s *Store) DeleteAllEnvVars(sandboxID string) error {
	if err := s.db.Where("sandbox_id = ?", sandboxID).Delete(&EnvVar{}).Error; err != nil {
		return fmt.Errorf("db: deleting all env vars for sandbox %s: %w", sandboxID, err)
	}
	return nil
}

// BulkUpsertEnvVars atomically inserts or updates a map of encrypted environment variables.
func (s *Store) BulkUpsertEnvVars(sandboxID string, vars map[string][]byte, aiSuggested bool) error {
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		for k, val := range vars {
			var existing EnvVar
			err := tx.Where("sandbox_id = ? AND key = ?", sandboxID, k).First(&existing).Error
			if err == gorm.ErrRecordNotFound {
				newVar := EnvVar{
					ID:            newID(),
					SandboxID:     sandboxID,
					Key:           k,
					Value:         val,
					IsAISuggested: aiSuggested,
					CreatedAt:     now,
					UpdatedAt:     now,
				}
				if err := tx.Create(&newVar).Error; err != nil {
					return fmt.Errorf("db: creating bulk env var %s: %w", k, err)
				}
			} else if err != nil {
				return fmt.Errorf("db: querying bulk env var %s: %w", k, err)
			} else {
				if err := tx.Model(&existing).Where("id = ?", existing.ID).
					Updates(map[string]any{"value": val, "is_ai_suggested": aiSuggested, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("db: updating bulk env var %s: %w", k, err)
				}
			}
		}
		return nil
	})
}

// ListEnvVars returns all env vars for a sandbox (encrypted values).
func (s *Store) ListEnvVars(sandboxID string) ([]*EnvVar, error) {
	var out []*EnvVar
	if err := s.db.Where("sandbox_id = ?", sandboxID).Order("key ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing env vars: %w", err)
	}
	return out, nil
}

// ---- sidecar databases --------------------------------------------------

// CreateDatabase records a provisioned sidecar database.
func (s *Store) CreateDatabase(d *SandboxDatabase) error {
	if d.ID == "" {
		d.ID = newID()
	}
	return s.db.Create(d).Error
}

// ListSandboxDatabases returns the sidecar DBs attached to a sandbox.
func (s *Store) ListSandboxDatabases(sandboxID string) ([]*SandboxDatabase, error) {
	var out []*SandboxDatabase
	if err := s.db.Where("sandbox_id = ?", sandboxID).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("db: listing sandbox databases: %w", err)
	}
	return out, nil
}

// DeleteSandboxDatabases drops the sidecar metadata for a sandbox. Teardown
// calls it after the containers and volumes are gone: without this, the row
// outlives the data it describes and a later deploy can believe a sidecar
// exists when it does not.
func (s *Store) DeleteSandboxDatabases(sandboxID string) (int64, error) {
	res := s.db.Where("sandbox_id = ?", sandboxID).Delete(&SandboxDatabase{})
	if res.Error != nil {
		return 0, fmt.Errorf("db: deleting sandbox databases for %s: %w", sandboxID, res.Error)
	}
	return res.RowsAffected, nil
}

// GetSandboxDatabase fetches one sidecar DB by id.
func (s *Store) GetSandboxDatabase(id string) (*SandboxDatabase, error) {
	d := &SandboxDatabase{}
	if err := s.db.First(d, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("db: fetching sandbox database %s: %w", id, err)
	}
	return d, nil
}

// ---- resource snapshots -------------------------------------------------

// InsertResourceSnapshots bulk-inserts buffered resource samples.
//
// A conflict on (sandbox_id, recorded_at) is expected rather than exceptional:
// the flush worker reads one Redis key per sandbox, so a collector that restarts
// between two passes can leave the key holding a sample the previous pass
// already stored. ON CONFLICT DO NOTHING makes that a no-op instead of aborting
// the whole batch, which would otherwise throw away every other sandbox's
// reading along with it.
func (s *Store) InsertResourceSnapshots(rows []*ResourceSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sandbox_id"}, {Name: "recorded_at"}},
		DoNothing: true,
	}).CreateInBatches(rows, 200).Error; err != nil {
		return fmt.Errorf("db: inserting resource snapshots: %w", err)
	}
	return nil
}

// DeleteResourceSnapshotsForSandbox drops every sample belonging to a sandbox.
// Teardown calls it so a destroyed sandbox does not leave behind rows that
// nothing will ever read; the cascade on sandbox_id cannot do this job, because
// the sandbox row survives as a tombstone.
func (s *Store) DeleteResourceSnapshotsForSandbox(sandboxID string) (int64, error) {
	res := s.db.Where("sandbox_id = ?", sandboxID).Delete(&ResourceSnapshot{})
	if res.Error != nil {
		return 0, fmt.Errorf("db: deleting resource snapshots for %s: %w", sandboxID, res.Error)
	}
	return res.RowsAffected, nil
}

// QueryResourceSnapshots returns samples within a window, oldest first.
//
// limit caps how many rows come back, and it is the newest ones that matter: a
// 24h window holds ~8,600 rows at the flush cadence, so `ORDER BY recorded_at
// ASC LIMIT n` would return the *oldest* n and the chart would show a stale
// two-hour slice from the start of the window. Selecting the newest n in an
// inner query and re-sorting ascending in the outer one keeps the cap while
// still handing back points in the order they were recorded.
func (s *Store) QueryResourceSnapshots(sandboxID string, since, until time.Time, limit int) ([]*ResourceSnapshot, error) {
	var out []*ResourceSnapshot
	const q = `SELECT * FROM (
		SELECT * FROM resource_snapshots
		WHERE sandbox_id = ? AND recorded_at BETWEEN ? AND ?
		ORDER BY recorded_at DESC
		LIMIT ?
	) AS recent
	ORDER BY recent.recorded_at ASC`
	if err := s.db.Raw(q, sandboxID, since, until, limit).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("db: querying resource snapshots: %w", err)
	}
	return out, nil
}

// DeleteResourceSnapshotsBefore drops samples older than the cutoff and returns
// how many went. resource_snapshots is append-only and would otherwise grow
// forever; the snapshot reaper calls this on a timer. The recorded_at index
// makes the range delete cheap.
func (s *Store) DeleteResourceSnapshotsBefore(cutoff time.Time) (int64, error) {
	res := s.db.Where("recorded_at < ?", cutoff).Delete(&ResourceSnapshot{})
	if res.Error != nil {
		return 0, fmt.Errorf("db: deleting resource snapshots older than %s: %w",
			cutoff.UTC().Format(time.RFC3339), res.Error)
	}
	return res.RowsAffected, nil
}

// ---- file edits ---------------------------------------------------------

// RecordFileEdit logs that a user edited a file in a sandbox.
func (s *Store) RecordFileEdit(sandboxID, path, userID string) error {
	e := &FileEdit{ID: newID(), SandboxID: sandboxID, FilePath: path, EditedBy: userID}
	return s.db.Create(e).Error
}

// MarkFileEditsPushed sets pushed_to_github for a sandbox's pending edits.
func (s *Store) MarkFileEditsPushed(sandboxID string) error {
	return s.db.Model(&FileEdit{}).
		Where("sandbox_id = ? AND pushed_to_github = ?", sandboxID, false).
		Update("pushed_to_github", true).Error
}

// ---- build queue recovery & reconciliation ------------------------------

// RecoverStuckDeployments reconciles in-flight deployments and building sandboxes
// after a worker or backend process restart, or after exceeding a heartbeat timeout.
// It marks any pending/building deployment as failed and transitions any
// queued/building sandbox to failed.
func (s *Store) RecoverStuckDeployments(olderThan time.Duration, reason string) (int64, error) {
	if reason == "" {
		reason = "build aborted: worker process restarted while build was in progress"
	}
	now := time.Now().UTC()

	dQuery := s.db.Model(&Deployment{}).Where("status IN ?", []string{string(DeployPending), string(DeployBuilding)})
	if olderThan > 0 {
		cutoff := now.Add(-olderThan)
		dQuery = dQuery.Where("started_at < ?", cutoff)
	}

	res := dQuery.Updates(map[string]any{
		"status":      string(DeployFailed),
		"log_excerpt": reason,
		"finished_at": now,
	})
	if res.Error != nil {
		return 0, fmt.Errorf("db: recovering stuck deployments: %w", res.Error)
	}
	affected := res.RowsAffected

	sbQuery := s.db.Model(&Sandbox{}).
		Where("status IN ? AND destroyed_at IS NULL", []SandboxStatus{StatusQueued, StatusBuilding})
	if olderThan > 0 {
		cutoff := now.Add(-olderThan)
		sbQuery = sbQuery.Where("updated_at < ?", cutoff)
	}
	if err := sbQuery.Updates(map[string]any{
		"status": string(StatusFailed),
	}).Error; err != nil {
		return affected, fmt.Errorf("db: recovering stuck sandboxes: %w", err)
	}

	return affected, nil
}

// ---- Projects & Collaboration -------------------------------------------

// GetOrCreateDefaultProject retrieves or creates the authenticated user's private personal project.
func (s *Store) GetOrCreateDefaultProject(userID string) (*Project, error) {
	if userID == "" {
		return nil, errors.New("db: user ID required for default project")
	}

	var p Project
	err := s.db.Where("created_by = ? AND is_default = true", userID).
		Preload("Members.User").
		Preload("Repositories").
		Preload("Sandboxes").
		First(&p).Error

	if err == nil {
		return &p, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("db: fetching default project: %w", err)
	}

	// Create default personal project
	p = Project{
		ID:          newID(),
		Name:        "Default Project (Personal)",
		Description: "Personal private development environment",
		IsDefault:   true,
		CreatedBy:   userID,
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		member := ProjectMember{
			ID:        newID(),
			ProjectID: p.ID,
			UserID:    userID,
			Role:      ProjectMemberRoleOwner,
			Status:    ProjectMemberStatusActive,
		}
		return tx.Create(&member).Error
	})
	if err != nil {
		return nil, fmt.Errorf("db: creating default project: %w", err)
	}

	return s.GetProject(p.ID)
}

// CreateProject creates a new user project and assigns the creator as OWNER.
func (s *Store) CreateProject(p *Project, userID string) (*Project, error) {
	if p.Name == "" {
		return nil, errors.New("db: project name cannot be empty")
	}
	if userID == "" {
		return nil, errors.New("db: creator user ID required")
	}

	p.ID = newID()
	p.CreatedBy = userID
	p.IsDefault = false

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(p).Error; err != nil {
			return err
		}
		member := ProjectMember{
			ID:        newID(),
			ProjectID: p.ID,
			UserID:    userID,
			Role:      ProjectMemberRoleOwner,
			Status:    ProjectMemberStatusActive,
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		return tx.Create(&AuditEvent{
			ID:        newID(),
			ProjectID: &p.ID,
			UserID:    &userID,
			Action:    "PROJECT_CREATE",
			Resource:  "project:" + p.ID,
		}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("db: creating project: %w", err)
	}

	return s.GetProject(p.ID)
}

// GetProject returns a project with members, repositories, and sandboxes loaded.
func (s *Store) GetProject(projectID string) (*Project, error) {
	var p Project
	err := s.db.Where("id = ?", projectID).
		Preload("Members.User").
		Preload("Repositories").
		Preload("Sandboxes").
		First(&p).Error
	if err != nil {
		return nil, fmt.Errorf("db: get project %s: %w", projectID, err)
	}
	return &p, nil
}

// ListUserProjects returns all projects accessible to the user (owned or shared membership).
func (s *Store) ListUserProjects(userID string) ([]*Project, error) {
	if userID == "" {
		return nil, errors.New("db: user ID required to list projects")
	}

	// Ensure user's default personal space exists
	_, _ = s.GetOrCreateDefaultProject(userID)

	var projects []*Project
	err := s.db.Distinct().
		Joins("LEFT JOIN project_members ON project_members.project_id = projects.id").
		Where("projects.created_by = ? OR (project_members.user_id = ? AND project_members.status = 'ACTIVE')", userID, userID).
		Preload("Members.User").
		Preload("Repositories").
		Preload("Sandboxes").
		Order("projects.is_default DESC, projects.created_at ASC").
		Find(&projects).Error

	if err != nil {
		return nil, fmt.Errorf("db: listing user projects: %w", err)
	}
	return projects, nil
}

// UpdateProject updates project name and description. Only Owner can modify.
func (s *Store) UpdateProject(projectID, userID, name, description string) (*Project, error) {
	p, err := s.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	role, err := s.GetProjectRole(projectID, userID)
	if err != nil || role != ProjectMemberRoleOwner {
		return nil, errors.New("access denied: only the project owner can modify settings")
	}

	updates := map[string]any{
		"updated_at": time.Now().UTC(),
	}
	if name != "" {
		updates["name"] = name
	}
	if description != "" {
		updates["description"] = description
	}

	if err := s.db.Model(p).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("db: updating project: %w", err)
	}

	_ = s.RecordAuditEvent(&AuditEvent{
		ID:        newID(),
		ProjectID: &projectID,
		UserID:    &userID,
		Action:    "PROJECT_UPDATE",
		Resource:  "project:" + projectID,
	})

	return s.GetProject(projectID)
}

// DeleteProject deletes a project and cascades cleanup. Default project cannot be deleted.
func (s *Store) DeleteProject(projectID, userID string) error {
	p, err := s.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.IsDefault {
		return errors.New("cannot delete default personal project")
	}
	role, err := s.GetProjectRole(projectID, userID)
	if err != nil || role != ProjectMemberRoleOwner {
		return errors.New("access denied: only the project owner can delete this project")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&Project{}, "id = ?", projectID).Error; err != nil {
			return err
		}
		return tx.Create(&AuditEvent{
			ID:       newID(),
			UserID:   &userID,
			Action:   "PROJECT_DELETE",
			Resource: "project:" + projectID,
		}).Error
	})
}

// AddProjectRepository associates a Git repository with a project.
func (s *Store) AddProjectRepository(repo *ProjectRepository) error {
	if repo.ID == "" {
		repo.ID = newID()
	}
	return s.db.Create(repo).Error
}

// ListProjectRepositories returns all repositories connected to a project.
func (s *Store) ListProjectRepositories(projectID string) ([]*ProjectRepository, error) {
	var repos []*ProjectRepository
	err := s.db.Where("project_id = ?", projectID).Find(&repos).Error
	return repos, err
}

// RemoveProjectRepository disassociates a repository from a project.
func (s *Store) RemoveProjectRepository(projectID, repoID string) error {
	return s.db.Delete(&ProjectRepository{}, "project_id = ? AND id = ?", projectID, repoID).Error
}

// AssociateProjectSandbox links a sandbox to a project.
func (s *Store) AssociateProjectSandbox(projectID, sandboxID string) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sandbox_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"project_id"}),
	}).Create(&ProjectSandbox{
		ProjectID: projectID,
		SandboxID: sandboxID,
		CreatedAt: time.Now().UTC(),
	}).Error
}

// DisassociateProjectSandbox removes a sandbox link from a project.
func (s *Store) DisassociateProjectSandbox(projectID, sandboxID string) error {
	return s.db.Delete(&ProjectSandbox{}, "project_id = ? AND sandbox_id = ?", projectID, sandboxID).Error
}

// GetProjectIDForSandbox returns the project ID associated with a sandbox.
func (s *Store) GetProjectIDForSandbox(sandboxID string) (string, error) {
	var ps ProjectSandbox
	err := s.db.Where("sandbox_id = ?", sandboxID).First(&ps).Error
	if err != nil {
		return "", err
	}
	return ps.ProjectID, nil
}

// GetProjectIDsForSandboxes batch-resolves project IDs for multiple sandboxes to eliminate N+1 queries.
func (s *Store) GetProjectIDsForSandboxes(sandboxIDs []string) (map[string]string, error) {
	if len(sandboxIDs) == 0 {
		return map[string]string{}, nil
	}
	var records []ProjectSandbox
	if err := s.db.Where("sandbox_id IN ?", sandboxIDs).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("db: fetching project sandbox associations: %w", err)
	}
	out := make(map[string]string, len(records))
	for _, r := range records {
		out[r.SandboxID] = r.ProjectID
	}
	return out, nil
}

// ListProjectSandboxes returns all active sandboxes associated with a project.
func (s *Store) ListProjectSandboxes(projectID string) ([]*Sandbox, error) {
	var sandboxes []*Sandbox
	err := s.db.Joins("JOIN project_sandboxes ON project_sandboxes.sandbox_id = sandboxes.id").
		Where("project_sandboxes.project_id = ? AND sandboxes.destroyed_at IS NULL", projectID).
		Find(&sandboxes).Error
	return sandboxes, err
}

// TransferSandbox moves an existing sandbox to destinationProjectID within an atomic database transaction.
func (s *Store) TransferSandbox(sandboxID, destinationProjectID, actorUserID string) error {
	sandboxID = strings.TrimSpace(sandboxID)
	destinationProjectID = strings.TrimSpace(destinationProjectID)
	actorUserID = strings.TrimSpace(actorUserID)

	if sandboxID == "" || destinationProjectID == "" || actorUserID == "" {
		return errors.New("sandboxID, destinationProjectID, and actorUserID are required")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock sandbox row and verify existence + status
		var sb Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", sandboxID).First(&sb).Error; err != nil {
			return fmt.Errorf("sandbox not found: %w", err)
		}
		if sb.DestroyedAt != nil || sb.Status == StatusDeleted || sb.Status == StatusExpired {
			return errors.New("cannot transfer a destroyed or expired sandbox")
		}

		// 2. Lock and retrieve current project association
		var currentPS ProjectSandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sandbox_id = ?", sandboxID).First(&currentPS).Error; err != nil {
			return errors.New("sandbox has missing project association; cannot transfer")
		}
		currentProjectID := currentPS.ProjectID

		// 3. Idempotency: if already in destination project, return nil
		if currentProjectID == destinationProjectID {
			return nil
		}

		// 4. Verify actor's authority: must be direct sandbox owner OR OWNER in source project
		owns, err := s.OwnsSandbox(sandboxID, actorUserID)
		if err != nil {
			return fmt.Errorf("checking ownership: %w", err)
		}
		if !owns {
			role, roleErr := s.GetProjectRole(currentProjectID, actorUserID)
			if roleErr != nil || role != ProjectMemberRoleOwner {
				return errors.New("access denied: only the sandbox owner or current project owner can transfer this sandbox")
			}
		}

		// 5. Verify destination project exists
		var destProj Project
		if err := tx.Where("id = ?", destinationProjectID).First(&destProj).Error; err != nil {
			return fmt.Errorf("destination project not found: %w", err)
		}

		// 6. Verify actor has OWNER or EDITOR role in destination project
		destRole, err := s.GetProjectRole(destinationProjectID, actorUserID)
		if err != nil || (destRole != ProjectMemberRoleOwner && destRole != ProjectMemberRoleEditor) {
			return errors.New("access denied: you must be an owner or editor in the destination project")
		}

		// 7. Atomically move association in project_sandboxes
		if err := tx.Delete(&ProjectSandbox{}, "sandbox_id = ?", sandboxID).Error; err != nil {
			return fmt.Errorf("removing existing project sandbox link: %w", err)
		}

		newPS := &ProjectSandbox{
			ProjectID: destinationProjectID,
			SandboxID: sandboxID,
			CreatedAt: time.Now().UTC(),
		}
		if err := tx.Create(newPS).Error; err != nil {
			return fmt.Errorf("associating sandbox with destination project: %w", err)
		}

		// Note: The source repository association is NOT modified (Mandatory Rules 1 & 5).

		// 8. Record structured audit event
		detailsJSON, _ := json.Marshal(map[string]any{
			"sandboxId":            sandboxID,
			"sourceProjectId":      currentProjectID,
			"destinationProjectId": destinationProjectID,
			"destinationName":      destProj.Name,
			"actorUserId":          actorUserID,
			"outcome":              "SUCCESS",
		})
		audit := &AuditEvent{
			ID:        newID(),
			ProjectID: &destinationProjectID,
			UserID:    &actorUserID,
			Action:    "TRANSFER_SANDBOX",
			Resource:  "sandbox:" + sandboxID,
			Details:   datatypes.JSON(detailsJSON),
			CreatedAt: time.Now().UTC(),
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("recording audit event: %w", err)
		}

		return nil
	})
}

// GetProjectRole returns the user's role in a project (OWNER, EDITOR, VIEWER).
func (s *Store) GetProjectRole(projectID, userID string) (ProjectMemberRole, error) {
	if projectID == "" || userID == "" {
		return "", errors.New("db: project and user ID required")
	}

	var p Project
	if err := s.db.Select("id, created_by, is_default").Where("id = ?", projectID).First(&p).Error; err == nil {
		if p.CreatedBy == userID {
			return ProjectMemberRoleOwner, nil
		}
	}

	var member ProjectMember
	err := s.db.Where("project_id = ? AND user_id = ? AND status = 'ACTIVE'", projectID, userID).First(&member).Error
	if err != nil {
		return "", fmt.Errorf("db: member not found: %w", err)
	}
	return member.Role, nil
}

// GetSandboxRole returns the user's highest effective role for a sandbox.
func (s *Store) GetSandboxRole(sandboxID, userID string) (ProjectMemberRole, error) {
	if sandboxID == "" || userID == "" {
		return "", errors.New("db: sandbox and user ID required")
	}

	// 1. Direct ownership
	owns, err := s.OwnsSandbox(sandboxID, userID)
	if err == nil && owns {
		return ProjectMemberRoleOwner, nil
	}

	// 2. Project membership
	var roles []ProjectMemberRole
	err = s.db.Model(&ProjectMember{}).
		Select("project_members.role").
		Joins("JOIN project_sandboxes ON project_sandboxes.project_id = project_members.project_id").
		Where("project_sandboxes.sandbox_id = ? AND project_members.user_id = ? AND project_members.status = 'ACTIVE'", sandboxID, userID).
		Pluck("role", &roles).Error

	if err == nil && len(roles) > 0 {
		for _, r := range roles {
			if r == ProjectMemberRoleOwner {
				return ProjectMemberRoleOwner, nil
			}
		}
		for _, r := range roles {
			if r == ProjectMemberRoleEditor {
				return ProjectMemberRoleEditor, nil
			}
		}
		return ProjectMemberRoleViewer, nil
	}

	return "", errors.New("access denied: user has no authorized role for this sandbox")
}

// CanManageSecretsForSandbox determines whether a user has permission to manage or reveal
// environment variables and secrets for the sandbox (Owner or explicitly granted Secrets Manager).
func (s *Store) CanManageSecretsForSandbox(sandboxID, userID string) (bool, error) {
	if sandboxID == "" || userID == "" {
		return false, errors.New("db: sandbox and user ID required")
	}

	// 1. Direct ownership grants full secrets management
	owns, err := s.OwnsSandbox(sandboxID, userID)
	if err == nil && owns {
		return true, nil
	}

	// 2. Project membership: OWNER role or explicitly granted can_manage_secrets
	type memberPerm struct {
		Role             ProjectMemberRole
		CanManageSecrets bool
	}
	var perms []memberPerm
	err = s.db.Model(&ProjectMember{}).
		Select("project_members.role, project_members.can_manage_secrets").
		Joins("JOIN project_sandboxes ON project_sandboxes.project_id = project_members.project_id").
		Where("project_sandboxes.sandbox_id = ? AND project_members.user_id = ? AND project_members.status = 'ACTIVE'", sandboxID, userID).
		Scan(&perms).Error

	if err == nil && len(perms) > 0 {
		for _, p := range perms {
			if p.Role == ProjectMemberRoleOwner || p.CanManageSecrets {
				return true, nil
			}
		}
	}
	return false, nil
}

// ListProjectMembers returns all active project members with populated User info.
func (s *Store) ListProjectMembers(projectID string) ([]*ProjectMember, error) {
	var members []*ProjectMember
	err := s.db.Where("project_id = ?", projectID).
		Preload("User").
		Order("role ASC, created_at ASC").
		Find(&members).Error
	return members, err
}

// UpdateMemberRole modifies a member's role (Editor <-> Viewer). Only Owner can execute.
func (s *Store) UpdateMemberRole(projectID, targetUserID string, newRole ProjectMemberRole, actorID string) error {
	actorRole, err := s.GetProjectRole(projectID, actorID)
	if err != nil || actorRole != ProjectMemberRoleOwner {
		return errors.New("access denied: only the project owner can change member roles")
	}

	p, err := s.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.CreatedBy == targetUserID {
		return errors.New("cannot change role of the project creator")
	}

	return s.db.Model(&ProjectMember{}).
		Where("project_id = ? AND user_id = ?", projectID, targetUserID).
		Updates(map[string]any{
			"role":       newRole,
			"updated_at": time.Now().UTC(),
		}).Error
}

// RemoveProjectMember removes a member from a project.
func (s *Store) RemoveProjectMember(projectID, targetUserID, actorID string) error {
	actorRole, err := s.GetProjectRole(projectID, actorID)
	if err != nil {
		return err
	}
	if actorRole != ProjectMemberRoleOwner && actorID != targetUserID {
		return errors.New("access denied: only the project owner or the member themselves can remove membership")
	}

	p, err := s.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.CreatedBy == targetUserID {
		return errors.New("cannot remove the project creator from the project")
	}

	return s.db.Delete(&ProjectMember{}, "project_id = ? AND user_id = ?", projectID, targetUserID).Error
}

// TransferProjectOwnership transfers project ownership to another registered user.
func (s *Store) TransferProjectOwnership(projectID, newOwnerID, currentOwnerID string) error {
	p, err := s.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.CreatedBy != currentOwnerID {
		return errors.New("access denied: only the current project owner can transfer ownership")
	}
	if p.IsDefault {
		return errors.New("cannot transfer ownership of the default personal space")
	}
	if newOwnerID == currentOwnerID {
		return errors.New("new owner must be different from current owner")
	}

	// Verify new owner is a registered user
	if _, err := s.GetUser(newOwnerID); err != nil {
		return fmt.Errorf("new owner must be a valid registered user: %w", err)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Update project created_by
		if err := tx.Model(p).Update("created_by", newOwnerID).Error; err != nil {
			return err
		}
		// 2. Demote current owner to EDITOR
		if err := tx.Model(&ProjectMember{}).
			Where("project_id = ? AND user_id = ?", projectID, currentOwnerID).
			Update("role", ProjectMemberRoleEditor).Error; err != nil {
			return err
		}
		// 3. Promote or insert new owner as OWNER
		var newMember ProjectMember
		err := tx.Where("project_id = ? AND user_id = ?", projectID, newOwnerID).First(&newMember).Error
		if err == nil {
			if err := tx.Model(&newMember).Update("role", ProjectMemberRoleOwner).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&ProjectMember{
				ID:        newID(),
				ProjectID: projectID,
				UserID:    newOwnerID,
				Role:      ProjectMemberRoleOwner,
				Status:    ProjectMemberStatusActive,
			}).Error; err != nil {
				return err
			}
		}

		return tx.Create(&AuditEvent{
			ID:        newID(),
			ProjectID: &projectID,
			UserID:    &currentOwnerID,
			Action:    "PROJECT_TRANSFER_OWNERSHIP",
			Resource:  fmt.Sprintf("project:%s:to:%s", projectID, newOwnerID),
		}).Error
	})
}

// CreateInvitation creates an atomic pending collaboration invitation.
func (s *Store) CreateInvitation(inv *ProjectInvitation) error {
	p, err := s.GetProject(inv.ProjectID)
	if err != nil {
		return err
	}
	if p.IsDefault {
		return errors.New("cannot invite collaborators to a default personal project")
	}

	role, err := s.GetProjectRole(inv.ProjectID, inv.InviterID)
	if err != nil || role != ProjectMemberRoleOwner {
		return errors.New("access denied: only the project owner can send member invitations")
	}

	if inv.InviteeID == inv.InviterID {
		return errors.New("cannot invite yourself to a project")
	}

	// Verify recipient exists
	if _, err := s.GetUser(inv.InviteeID); err != nil {
		return fmt.Errorf("recipient user not found: %w", err)
	}

	// Verify recipient is not already an active member
	var existingMember ProjectMember
	if err := s.db.Where("project_id = ? AND user_id = ? AND status = 'ACTIVE'", inv.ProjectID, inv.InviteeID).First(&existingMember).Error; err == nil {
		return errors.New("user is already an active member of this project")
	}

	// Verify no pending invitation exists
	var pendingInv ProjectInvitation
	if err := s.db.Where("project_id = ? AND invitee_id = ? AND status = 'PENDING' AND expires_at > ?", inv.ProjectID, inv.InviteeID, time.Now().UTC()).First(&pendingInv).Error; err == nil {
		return errors.New("an active invitation is already pending for this user")
	}

	inv.ID = newID()
	inv.Status = InvitePending
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = time.Now().UTC().Add(7 * 24 * time.Hour)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(inv).Error; err != nil {
			return err
		}
		return tx.Create(&AuditEvent{
			ID:        newID(),
			ProjectID: &inv.ProjectID,
			UserID:    &inv.InviterID,
			Action:    "INVITATION_CREATE",
			Resource:  fmt.Sprintf("invitation:%s:invitee:%s", inv.ID, inv.InviteeID),
		}).Error
	})
}

// GetInvitation fetches an invitation by ID with preloads.
func (s *Store) GetInvitation(id string) (*ProjectInvitation, error) {
	var inv ProjectInvitation
	err := s.db.Where("id = ?", id).
		Preload("Project").
		Preload("Inviter").
		Preload("Invitee").
		First(&inv).Error
	if err != nil {
		return nil, fmt.Errorf("db: get invitation %s: %w", id, err)
	}
	return &inv, nil
}

// ListPendingProjectInvitations returns active pending invitations for a project.
func (s *Store) ListPendingProjectInvitations(projectID string) ([]*ProjectInvitation, error) {
	var invs []*ProjectInvitation
	err := s.db.Where("project_id = ? AND status = 'PENDING' AND expires_at > ?", projectID, time.Now().UTC()).
		Preload("Invitee").
		Preload("Inviter").
		Order("created_at DESC").
		Find(&invs).Error
	return invs, err
}

// ListUserInvitations returns pending invitations received by the user.
func (s *Store) ListUserInvitations(userID string) ([]*ProjectInvitation, error) {
	var invs []*ProjectInvitation
	err := s.db.Where("invitee_id = ? AND status = 'PENDING' AND expires_at > ?", userID, time.Now().UTC()).
		Preload("Project").
		Preload("Inviter").
		Order("created_at DESC").
		Find(&invs).Error
	return invs, err
}

// AcceptInvitation atomically accepts an invitation and adds the user to project_members.
func (s *Store) AcceptInvitation(invitationID, userID string) error {
	now := time.Now().UTC()

	return s.db.Transaction(func(tx *gorm.DB) error {
		var inv ProjectInvitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", invitationID).First(&inv).Error; err != nil {
			return fmt.Errorf("invitation not found: %w", err)
		}

		if inv.InviteeID != userID {
			return errors.New("access denied: this invitation was addressed to another user")
		}
		if inv.Status != InvitePending {
			return fmt.Errorf("invitation cannot be accepted (current status: %s)", inv.Status)
		}
		if now.After(inv.ExpiresAt) {
			_ = tx.Model(&inv).Update("status", InviteExpired)
			return errors.New("invitation has expired")
		}

		// 1. Mark invitation accepted
		if err := tx.Model(&inv).Updates(map[string]any{
			"status":      InviteAccepted,
			"accepted_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}

		// 2. Add or update project_members row
		var member ProjectMember
		err := tx.Where("project_id = ? AND user_id = ?", inv.ProjectID, userID).First(&member).Error
		if err == nil {
			if err := tx.Model(&member).Updates(map[string]any{
				"role":        inv.Role,
				"status":      ProjectMemberStatusActive,
				"accepted_at": now,
				"updated_at":  now,
			}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&ProjectMember{
				ID:         newID(),
				ProjectID:  inv.ProjectID,
				UserID:     userID,
				Role:       inv.Role,
				Status:     ProjectMemberStatusActive,
				InvitedBy:  &inv.InviterID,
				AcceptedAt: &now,
			}).Error; err != nil {
				return err
			}
		}

		// 3. Audit trail
		return tx.Create(&AuditEvent{
			ID:        newID(),
			ProjectID: &inv.ProjectID,
			UserID:    &userID,
			Action:    "INVITATION_ACCEPT",
			Resource:  "invitation:" + invitationID,
		}).Error
	})
}

// RejectInvitation marks an invitation rejected.
func (s *Store) RejectInvitation(invitationID, userID string) error {
	inv, err := s.GetInvitation(invitationID)
	if err != nil {
		return err
	}
	if inv.InviteeID != userID {
		return errors.New("access denied: cannot reject invitation addressed to another user")
	}

	return s.db.Model(inv).Updates(map[string]any{
		"status":     InviteRejected,
		"updated_at": time.Now().UTC(),
	}).Error
}

// RevokeInvitation allows project owner or inviter to cancel a pending invitation.
func (s *Store) RevokeInvitation(invitationID, actorID string) error {
	inv, err := s.GetInvitation(invitationID)
	if err != nil {
		return err
	}

	role, err := s.GetProjectRole(inv.ProjectID, actorID)
	if err != nil || (role != ProjectMemberRoleOwner && inv.InviterID != actorID) {
		return errors.New("access denied: only the project owner or inviter can revoke this invitation")
	}

	return s.db.Model(inv).Updates(map[string]any{
		"status":     InviteRevoked,
		"updated_at": time.Now().UTC(),
	}).Error
}

// ResendInvitation resets the expiration of a pending invitation.
func (s *Store) ResendInvitation(invitationID, actorID string) error {
	inv, err := s.GetInvitation(invitationID)
	if err != nil {
		return err
	}

	role, err := s.GetProjectRole(inv.ProjectID, actorID)
	if err != nil || (role != ProjectMemberRoleOwner && inv.InviterID != actorID) {
		return errors.New("access denied: only the project owner or inviter can resend this invitation")
	}

	return s.db.Model(inv).Updates(map[string]any{
		"status":     InvitePending,
		"expires_at": time.Now().UTC().Add(7 * 24 * time.Hour),
		"updated_at": time.Now().UTC(),
	}).Error
}

// RecordAuditEvent writes an immutable audit record.
func (s *Store) RecordAuditEvent(event *AuditEvent) error {
	if event.ID == "" {
		event.ID = newID()
	}
	return s.db.Create(event).Error
}

// ListAuditEvents lists audit events for a project.
func (s *Store) ListAuditEvents(projectID string, limit int) ([]*AuditEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var events []*AuditEvent
	err := s.db.Where("project_id = ?", projectID).
		Order("created_at DESC").
		Limit(limit).
		Find(&events).Error
	return events, err
}
