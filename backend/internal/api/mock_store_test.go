package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// unifiedMockStore is a thread-safe, in-memory implementation of the Store interface
// used across all API unit and integration test suites.
type unifiedMockStore struct {
	mu                  sync.RWMutex
	users               map[string]*db.User
	projects            map[string]*db.Project
	members             map[string]map[string]*db.ProjectMember // projectID -> userID -> member
	sandboxes           map[string]*db.Sandbox
	projectSandboxes    map[string]string                       // sandboxID -> projectID
	sbProjects          map[string]string                       // alias for backward compatibility
	repositories        map[string]*db.Repository
	projectRepositories map[string][]*db.ProjectRepository      // projectID -> list of ProjectRepository
	envVars             map[string]map[string]*db.EnvVar        // sandboxID -> key -> EnvVar
	invitations         map[string]*db.ProjectInvitation        // id -> invitation
	deployments         map[string][]*db.Deployment             // sandboxID -> list of Deployments
	databases           map[string][]*db.SandboxDatabase        // sandboxID -> list of Databases
	auditEvents         []*db.AuditEvent
}

func newUnifiedMockStore() *unifiedMockStore {
	ps := make(map[string]string)
	return &unifiedMockStore{
		users:               make(map[string]*db.User),
		projects:            make(map[string]*db.Project),
		members:             make(map[string]map[string]*db.ProjectMember),
		sandboxes:           make(map[string]*db.Sandbox),
		projectSandboxes:    ps,
		sbProjects:          ps,
		repositories:        make(map[string]*db.Repository),
		projectRepositories: make(map[string][]*db.ProjectRepository),
		envVars:             make(map[string]map[string]*db.EnvVar),
		invitations:         make(map[string]*db.ProjectInvitation),
		deployments:         make(map[string][]*db.Deployment),
		databases:           make(map[string][]*db.SandboxDatabase),
		auditEvents:         make([]*db.AuditEvent, 0),
	}
}

type mockProjectsStore = unifiedMockStore

func newMockProjectsStore() *mockProjectsStore {
	return newUnifiedMockStore()
}

// Seed Helpers
func (m *unifiedMockStore) seedUser(id, username, email string) *db.User {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := &db.User{
		ID:        id,
		Username:  username,
		Email:     email,
		CreatedAt: time.Now(),
	}
	m.users[id] = u
	return u
}

func (m *unifiedMockStore) seedProject(id, name, createdBy string, isDefault bool) *db.Project {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &db.Project{
		ID:        id,
		Name:      name,
		CreatedBy: createdBy,
		IsDefault: isDefault,
		CreatedAt: time.Now(),
	}
	m.projects[id] = p
	if m.members[id] == nil {
		m.members[id] = make(map[string]*db.ProjectMember)
	}
	m.members[id][createdBy] = &db.ProjectMember{
		ID:        uuid.New().String(),
		ProjectID: id,
		UserID:    createdBy,
		Role:      db.ProjectMemberRoleOwner,
		Status:    db.ProjectMemberStatusActive,
		CreatedAt: time.Now(),
	}
	return p
}

func (m *unifiedMockStore) seedMember(projectID, userID string, role db.ProjectMemberRole) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[projectID] == nil {
		m.members[projectID] = make(map[string]*db.ProjectMember)
	}
	m.members[projectID][userID] = &db.ProjectMember{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		UserID:    userID,
		Role:      role,
		Status:    db.ProjectMemberStatusActive,
		CreatedAt: time.Now(),
	}
}

func (m *unifiedMockStore) seedSandbox(sb *db.Sandbox, projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sb.ID == "" {
		sb.ID = uuid.New().String()
	}
	if sb.Name == "" {
		sb.Name = sb.Subdomain
	}
	if sb.Slug == "" {
		sb.Slug = sb.Subdomain
	}
	m.sandboxes[sb.ID] = sb
	if projectID != "" {
		m.projectSandboxes[sb.ID] = projectID
	}
}

// Store Interface Implementation
func (m *unifiedMockStore) Ping(ctx context.Context) error {
	return nil
}

func (m *unifiedMockStore) UpsertUser(u *db.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[u.ID] = u
	return nil
}

func (m *unifiedMockStore) GetUserByGithubID(githubID int64) (*db.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.GithubID == githubID {
			return u, nil
		}
	}
	return nil, errors.New("user not found")
}

func (m *unifiedMockStore) GetUser(id string) (*db.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return u, nil
}

func (m *unifiedMockStore) SearchUsers(query string, limit int) ([]*db.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.User
	q := strings.ToLower(query)
	for _, u := range m.users {
		if strings.Contains(strings.ToLower(u.Username), q) || strings.Contains(strings.ToLower(u.Email), q) {
			res = append(res, u)
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

func (m *unifiedMockStore) OwnsSandbox(sandboxID, userID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sb, ok := m.sandboxes[sandboxID]
	if !ok {
		return false, errors.New("sandbox not found")
	}
	if repo, ok := m.repositories[sb.RepositoryID]; ok && repo.UserID == userID {
		return true, nil
	}
	if projID, ok := m.projectSandboxes[sandboxID]; ok {
		if mems, ok := m.members[projID]; ok {
			if mem, ok := mems[userID]; ok && mem.Role == db.ProjectMemberRoleOwner {
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *unifiedMockStore) GetSandbox(id string) (*db.Sandbox, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return sb, nil
}

func (m *unifiedMockStore) GetSandboxBySubdomain(subdomain string) (*db.Sandbox, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sb := range m.sandboxes {
		if sb.Subdomain == subdomain || sb.Slug == subdomain {
			return sb, nil
		}
	}
	return nil, errors.New("sandbox not found")
}

func (m *unifiedMockStore) ListSandboxes(userID string) ([]*db.Sandbox, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.Sandbox
	for _, sb := range m.sandboxes {
		if repo, ok := m.repositories[sb.RepositoryID]; ok && repo.UserID == userID {
			res = append(res, sb)
			continue
		}
		if projID, ok := m.projectSandboxes[sb.ID]; ok {
			if mems, ok := m.members[projID]; ok {
				if _, ok := mems[userID]; ok {
					res = append(res, sb)
				}
			}
		}
	}
	return res, nil
}

func (m *unifiedMockStore) FindOrCreateRepository(userID, cloneURL, defaultBranch string) (*db.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repositories {
		if r.UserID == userID && r.GithubURL == cloneURL {
			return r, nil
		}
	}
	repo := &db.Repository{
		ID:            uuid.New().String(),
		UserID:        userID,
		GithubURL:     cloneURL,
		DefaultBranch: defaultBranch,
		CreatedAt:     time.Now(),
	}
	m.repositories[repo.ID] = repo
	return repo, nil
}

func (m *unifiedMockStore) GetRepository(id string) (*db.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.repositories[id]
	if !ok {
		return nil, errors.New("repository not found")
	}
	return r, nil
}

func (m *unifiedMockStore) CreateSandbox(sb *db.Sandbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sb.ID == "" {
		sb.ID = uuid.New().String()
	}
	m.sandboxes[sb.ID] = sb
	return nil
}

func (m *unifiedMockStore) CreateSandboxWithinLimits(sb *db.Sandbox, userID string, maxActive, maxRunning int) error {
	return m.CreateSandbox(sb)
}

func (m *unifiedMockStore) CreateSandboxWithProject(sb *db.Sandbox, ownerUserID, projectID string, maxActive, maxRunning int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if projectID != "" {
		if _, ok := m.projects[projectID]; !ok {
			m.projects[projectID] = &db.Project{
				ID:        projectID,
				Name:      "Project " + projectID,
				CreatedBy: ownerUserID,
				CreatedAt: time.Now(),
			}
		}
	}
	if sb.ID == "" {
		sb.ID = uuid.New().String()
	}
	m.sandboxes[sb.ID] = sb
	if projectID != "" {
		m.projectSandboxes[sb.ID] = projectID
	}
	return nil
}

func (m *unifiedMockStore) GetProjectIDsForSandboxes(sandboxIDs []string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string]string)
	for _, id := range sandboxIDs {
		if pID, ok := m.projectSandboxes[id]; ok {
			res[id] = pID
		}
	}
	return res, nil
}

func (m *unifiedMockStore) UpsertEnvVar(sandboxID, key string, value []byte, aiSuggested bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envVars[sandboxID] == nil {
		m.envVars[sandboxID] = make(map[string]*db.EnvVar)
	}
	m.envVars[sandboxID][key] = &db.EnvVar{
		ID:            uuid.New().String(),
		SandboxID:     sandboxID,
		Key:           key,
		Value:         value,
		IsAISuggested: aiSuggested,
		UpdatedAt:     time.Now(),
	}
	return nil
}

func (m *unifiedMockStore) GetEnvVar(sandboxID, key string) (*db.EnvVar, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if sVars, ok := m.envVars[sandboxID]; ok {
		if v, ok := sVars[key]; ok {
			return v, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *unifiedMockStore) DeleteEnvVar(sandboxID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sVars, ok := m.envVars[sandboxID]; ok {
		delete(sVars, key)
	}
	return nil
}

func (m *unifiedMockStore) DeleteAllEnvVars(sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.envVars, sandboxID)
	return nil
}

func (m *unifiedMockStore) BulkUpsertEnvVars(sandboxID string, vars map[string][]byte, aiSuggested bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envVars[sandboxID] == nil {
		m.envVars[sandboxID] = make(map[string]*db.EnvVar)
	}
	for k, v := range vars {
		m.envVars[sandboxID][k] = &db.EnvVar{
			ID:            uuid.New().String(),
			SandboxID:     sandboxID,
			Key:           k,
			Value:         v,
			IsAISuggested: aiSuggested,
			UpdatedAt:     time.Now(),
		}
	}
	return nil
}

func (m *unifiedMockStore) ListEnvVars(sandboxID string) ([]*db.EnvVar, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*db.EnvVar, 0)
	if sVars, ok := m.envVars[sandboxID]; ok {
		for _, v := range sVars {
			res = append(res, v)
		}
	}
	return res, nil
}

func (m *unifiedMockStore) CanManageSecretsForSandbox(sandboxID, userID string) (bool, error) {
	role, err := m.GetSandboxRole(sandboxID, userID)
	if err != nil {
		return false, err
	}
	return role == db.ProjectMemberRoleOwner || role == db.ProjectMemberRoleEditor, nil
}

func (m *unifiedMockStore) RecordFileEdit(sandboxID, relPath, userID string) error {
	return nil
}

func (m *unifiedMockStore) MarkFileEditsPushed(sandboxID string) error {
	return nil
}

func (m *unifiedMockStore) ListSandboxDatabases(sandboxID string) ([]*db.SandboxDatabase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.databases[sandboxID], nil
}

func (m *unifiedMockStore) UpdateSandbox(id string, fields map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return errors.New("sandbox not found")
	}
	if name, ok := fields["name"].(string); ok {
		sb.Name = name
	}
	if slug, ok := fields["slug"].(string); ok {
		sb.Slug = slug
	}
	if status, ok := fields["status"].(string); ok {
		sb.Status = db.SandboxStatus(status)
	}
	return nil
}

func (m *unifiedMockStore) RenameSandbox(sandboxID, newName string) (*db.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[sandboxID]
	if !ok {
		return nil, errors.New("sandbox not found")
	}
	sb.Name = newName
	sb.Slug = strings.ToLower(strings.ReplaceAll(newName, " ", "-"))
	return sb, nil
}

func (m *unifiedMockStore) ListDeployments(sandboxID string) ([]*db.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deployments[sandboxID], nil
}

func (m *unifiedMockStore) ListWarningThresholds(sandboxID string) ([]string, error) {
	return []string{}, nil
}

func (m *unifiedMockStore) QueryResourceSnapshots(sandboxID string, since, until time.Time, limit int) ([]*db.ResourceSnapshot, error) {
	return []*db.ResourceSnapshot{}, nil
}

func (m *unifiedMockStore) UpdateLifecycleConfig(id string, idleTimeoutSec int, hibernatedExpirySec, retentionSec int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return errors.New("sandbox not found")
	}
	sb.IdleTimeoutSeconds = idleTimeoutSec
	return nil
}

func (m *unifiedMockStore) GetOrCreateDefaultProject(userID string) (*db.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.CreatedBy == userID && p.IsDefault {
			return p, nil
		}
	}
	p := &db.Project{
		ID:        "default-proj-" + userID,
		Name:      "Default Workspace",
		CreatedBy: userID,
		IsDefault: true,
		CreatedAt: time.Now(),
	}
	m.projects[p.ID] = p
	if m.members[p.ID] == nil {
		m.members[p.ID] = make(map[string]*db.ProjectMember)
	}
	m.members[p.ID][userID] = &db.ProjectMember{
		ID:        uuid.New().String(),
		ProjectID: p.ID,
		UserID:    userID,
		Role:      db.ProjectMemberRoleOwner,
		Status:    db.ProjectMemberStatusActive,
		CreatedAt: time.Now(),
	}
	return p, nil
}

func (m *unifiedMockStore) CreateProject(p *db.Project, userID string) (*db.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	p.CreatedBy = userID
	m.projects[p.ID] = p
	if m.members[p.ID] == nil {
		m.members[p.ID] = make(map[string]*db.ProjectMember)
	}
	m.members[p.ID][userID] = &db.ProjectMember{
		ID:        uuid.New().String(),
		ProjectID: p.ID,
		UserID:    userID,
		Role:      db.ProjectMemberRoleOwner,
		Status:    db.ProjectMemberStatusActive,
		CreatedAt: time.Now(),
	}
	return p, nil
}

func (m *unifiedMockStore) GetProject(projectID string) (*db.Project, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.projects[projectID]
	if !ok {
		return nil, errors.New("project not found")
	}
	return p, nil
}

func (m *unifiedMockStore) ListUserProjects(userID string) ([]*db.Project, error) {
	_, _ = m.GetOrCreateDefaultProject(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.Project
	for _, p := range m.projects {
		if p.CreatedBy == userID {
			res = append(res, p)
		} else if mems, ok := m.members[p.ID]; ok {
			if mem, ok := mems[userID]; ok && mem.Status == db.ProjectMemberStatusActive {
				res = append(res, p)
			}
		}
	}
	return res, nil
}

func (m *unifiedMockStore) UpdateProject(projectID, userID, name, description string) (*db.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[projectID]
	if !ok {
		return nil, errors.New("project not found")
	}
	p.Name = name
	p.Description = description
	return p, nil
}

func (m *unifiedMockStore) DeleteProject(projectID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projects, projectID)
	delete(m.members, projectID)
	return nil
}

func (m *unifiedMockStore) AddProjectRepository(repo *db.ProjectRepository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectRepositories[repo.ProjectID] = append(m.projectRepositories[repo.ProjectID], repo)
	return nil
}

func (m *unifiedMockStore) ListProjectRepositories(projectID string) ([]*db.ProjectRepository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.projectRepositories[projectID], nil
}

func (m *unifiedMockStore) RemoveProjectRepository(projectID, repoID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	repos := m.projectRepositories[projectID]
	var updated []*db.ProjectRepository
	for _, r := range repos {
		if r.ID != repoID {
			updated = append(updated, r)
		}
	}
	m.projectRepositories[projectID] = updated
	return nil
}

func (m *unifiedMockStore) AssociateProjectSandbox(projectID, sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if projectID != "" {
		if _, ok := m.projects[projectID]; !ok {
			m.projects[projectID] = &db.Project{
				ID:        projectID,
				Name:      "Project " + projectID,
				CreatedAt: time.Now(),
			}
		}
	}
	m.projectSandboxes[sandboxID] = projectID
	return nil
}

func (m *unifiedMockStore) DisassociateProjectSandbox(projectID, sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projectSandboxes, sandboxID)
	return nil
}

func (m *unifiedMockStore) TransferSandbox(sandboxID, destinationProjectID, actorUserID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[sandboxID]
	if !ok {
		return errors.New("sandbox not found")
	}
	if sb.DestroyedAt != nil || sb.Status == db.StatusDeleted || sb.Status == db.StatusExpired {
		return errors.New("cannot transfer a destroyed or expired sandbox")
	}
	currProjectID, ok := m.projectSandboxes[sandboxID]
	if !ok {
		return errors.New("missing project association")
	}
	mems, ok := m.members[currProjectID]
	if !ok || mems[actorUserID] == nil || (mems[actorUserID].Role != db.ProjectMemberRoleOwner && mems[actorUserID].Role != db.ProjectMemberRoleEditor) {
		return errors.New("access denied: actor must be Owner or Editor of current project")
	}
	destMems, ok := m.members[destinationProjectID]
	if !ok || destMems[actorUserID] == nil || (destMems[actorUserID].Role != db.ProjectMemberRoleOwner && destMems[actorUserID].Role != db.ProjectMemberRoleEditor) {
		return errors.New("access denied: actor must be Owner or Editor of destination project")
	}
	m.projectSandboxes[sandboxID] = destinationProjectID
	return nil
}

func (m *unifiedMockStore) GetProjectIDForSandbox(sandboxID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pID, ok := m.projectSandboxes[sandboxID]
	if !ok {
		return "", errors.New("no project associated with sandbox")
	}
	return pID, nil
}

func (m *unifiedMockStore) ListProjectSandboxes(projectID string) ([]*db.Sandbox, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.Sandbox
	for sID, pID := range m.projectSandboxes {
		if pID == projectID {
			if sb, ok := m.sandboxes[sID]; ok {
				res = append(res, sb)
			}
		}
	}
	return res, nil
}

func (m *unifiedMockStore) GetProjectRole(projectID, userID string) (db.ProjectMemberRole, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mems, ok := m.members[projectID]; ok {
		if mem, ok := mems[userID]; ok {
			return mem.Role, nil
		}
	}
	return "", errors.New("user is not a member of project")
}

func (m *unifiedMockStore) GetSandboxRole(sandboxID, userID string) (db.ProjectMemberRole, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pID, ok := m.projectSandboxes[sandboxID]
	if !ok {
		return "", errors.New("sandbox not associated with project")
	}
	if mems, ok := m.members[pID]; ok {
		if mem, ok := mems[userID]; ok {
			return mem.Role, nil
		}
	}
	return "", errors.New("user is not a member of project")
}

func (m *unifiedMockStore) ListProjectMembers(projectID string) ([]*db.ProjectMember, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.ProjectMember
	if mems, ok := m.members[projectID]; ok {
		for _, m := range mems {
			res = append(res, m)
		}
	}
	return res, nil
}

func (m *unifiedMockStore) UpdateMemberRole(projectID, targetUserID string, newRole db.ProjectMemberRole, actorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mems, ok := m.members[projectID]; ok {
		if mem, ok := mems[targetUserID]; ok {
			mem.Role = newRole
			return nil
		}
	}
	return errors.New("member not found")
}

func (m *unifiedMockStore) RemoveProjectMember(projectID, targetUserID, actorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mems, ok := m.members[projectID]; ok {
		delete(mems, targetUserID)
		return nil
	}
	return errors.New("project not found")
}

func (m *unifiedMockStore) TransferProjectOwnership(projectID, newOwnerID, currentOwnerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mems, ok := m.members[projectID]; ok {
		if mem, ok := mems[newOwnerID]; ok {
			mem.Role = db.ProjectMemberRoleOwner
		}
		if mem, ok := mems[currentOwnerID]; ok {
			mem.Role = db.ProjectMemberRoleEditor
		}
		return nil
	}
	return errors.New("project not found")
}

func (m *unifiedMockStore) CreateInvitation(inv *db.ProjectInvitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[inv.ProjectID]
	if !ok {
		return errors.New("project not found")
	}
	if p.IsDefault {
		return errors.New("cannot invite collaborators to a default personal project")
	}
	if mems, ok := m.members[inv.ProjectID]; !ok || mems[inv.InviterID] == nil || mems[inv.InviterID].Role != db.ProjectMemberRoleOwner {
		return errors.New("access denied: only the project owner can send member invitations")
	}
	if inv.InviteeID == inv.InviterID {
		return errors.New("cannot invite yourself to a project")
	}
	if _, ok := m.users[inv.InviteeID]; !ok {
		return errors.New("recipient user not found")
	}
	if inv.ID == "" {
		inv.ID = uuid.New().String()
	}
	m.invitations[inv.ID] = inv
	return nil
}

func (m *unifiedMockStore) GetInvitation(id string) (*db.ProjectInvitation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inv, ok := m.invitations[id]
	if !ok {
		return nil, errors.New("invitation not found")
	}
	return inv, nil
}

func (m *unifiedMockStore) ListPendingProjectInvitations(projectID string) ([]*db.ProjectInvitation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.ProjectInvitation
	for _, inv := range m.invitations {
		if inv.ProjectID == projectID && inv.Status == db.InvitePending {
			res = append(res, inv)
		}
	}
	return res, nil
}

func (m *unifiedMockStore) ListUserInvitations(userID string) ([]*db.ProjectInvitation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*db.ProjectInvitation
	for _, inv := range m.invitations {
		if inv.InviteeID == userID && inv.Status == db.InvitePending {
			res = append(res, inv)
		}
	}
	return res, nil
}

func (m *unifiedMockStore) AcceptInvitation(invitationID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	inv.Status = db.InviteAccepted
	if m.members[inv.ProjectID] == nil {
		m.members[inv.ProjectID] = make(map[string]*db.ProjectMember)
	}
	m.members[inv.ProjectID][userID] = &db.ProjectMember{
		ID:        uuid.New().String(),
		ProjectID: inv.ProjectID,
		UserID:    userID,
		Role:      inv.Role,
		Status:    db.ProjectMemberStatusActive,
		CreatedAt: time.Now(),
	}
	return nil
}

func (m *unifiedMockStore) RejectInvitation(invitationID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	inv.Status = db.InviteRejected
	return nil
}

func (m *unifiedMockStore) RevokeInvitation(invitationID, actorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	inv.Status = db.InviteRevoked
	return nil
}

func (m *unifiedMockStore) ResendInvitation(invitationID, actorID string) error {
	return nil
}

func (m *unifiedMockStore) RecordAuditEvent(event *db.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditEvents = append(m.auditEvents, event)
	return nil
}

func (m *unifiedMockStore) ListAuditEvents(projectID string, limit int) ([]*db.AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.auditEvents, nil
}
