package api

import (
	"context"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
)

// Store defines the database contract required by the API server.
// The production *db.Store satisfies this interface.
type Store interface {
	Ping(ctx context.Context) error
	UpsertUser(u *db.User) error
	GetUserByGithubID(githubID int64) (*db.User, error)
	GetUser(id string) (*db.User, error)
	SearchUsers(query string, limit int) ([]*db.User, error)
	OwnsSandbox(sandboxID, userID string) (bool, error)
	GetSandbox(id string) (*db.Sandbox, error)
	GetSandboxBySubdomain(subdomain string) (*db.Sandbox, error)
	ListSandboxes(userID string) ([]*db.Sandbox, error)
	FindOrCreateRepository(userID, cloneURL, defaultBranch string) (*db.Repository, error)
	GetRepository(id string) (*db.Repository, error)
	CreateSandbox(sb *db.Sandbox) error
	CreateSandboxWithinLimits(sb *db.Sandbox, userID string, maxActive, maxRunning int) error
	UpsertEnvVar(sandboxID, key string, value []byte, aiSuggested bool) error
	RecordFileEdit(sandboxID, relPath, userID string) error
	MarkFileEditsPushed(sandboxID string) error
	ListSandboxDatabases(sandboxID string) ([]*db.SandboxDatabase, error)
	UpdateSandbox(id string, fields map[string]any) error
	ListDeployments(sandboxID string) ([]*db.Deployment, error)
	ListWarningThresholds(sandboxID string) ([]string, error)
	QueryResourceSnapshots(sandboxID string, since, until time.Time, limit int) ([]*db.ResourceSnapshot, error)
	UpdateLifecycleConfig(id string, idleTimeoutSec int, hibernatedExpirySec, retentionSec int64) error

	// Project & Collaboration Management
	GetOrCreateDefaultProject(userID string) (*db.Project, error)
	CreateProject(p *db.Project, userID string) (*db.Project, error)
	GetProject(projectID string) (*db.Project, error)
	ListUserProjects(userID string) ([]*db.Project, error)
	UpdateProject(projectID, userID, name, description string) (*db.Project, error)
	DeleteProject(projectID, userID string) error
	AddProjectRepository(repo *db.ProjectRepository) error
	ListProjectRepositories(projectID string) ([]*db.ProjectRepository, error)
	RemoveProjectRepository(projectID, repoID string) error
	AssociateProjectSandbox(projectID, sandboxID string) error
	DisassociateProjectSandbox(projectID, sandboxID string) error
	GetProjectIDForSandbox(sandboxID string) (string, error)
	ListProjectSandboxes(projectID string) ([]*db.Sandbox, error)
	GetProjectRole(projectID, userID string) (db.ProjectMemberRole, error)
	GetSandboxRole(sandboxID, userID string) (db.ProjectMemberRole, error)
	ListProjectMembers(projectID string) ([]*db.ProjectMember, error)
	UpdateMemberRole(projectID, targetUserID string, newRole db.ProjectMemberRole, actorID string) error
	RemoveProjectMember(projectID, targetUserID, actorID string) error
	TransferProjectOwnership(projectID, newOwnerID, currentOwnerID string) error

	// Invitations
	CreateInvitation(inv *db.ProjectInvitation) error
	GetInvitation(id string) (*db.ProjectInvitation, error)
	ListPendingProjectInvitations(projectID string) ([]*db.ProjectInvitation, error)
	ListUserInvitations(userID string) ([]*db.ProjectInvitation, error)
	AcceptInvitation(invitationID, userID string) error
	RejectInvitation(invitationID, userID string) error
	RevokeInvitation(invitationID, actorID string) error
	ResendInvitation(invitationID, actorID string) error

	// Audit
	RecordAuditEvent(event *db.AuditEvent) error
	ListAuditEvents(projectID string, limit int) ([]*db.AuditEvent, error)
}
