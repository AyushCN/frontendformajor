package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/go-chi/chi/v5"
)

type mockProjectsStore struct {
	Store
	projects    map[string]*db.Project
	members     map[string]map[string]*db.ProjectMember // projectID -> userID -> member
	invitations map[string]*db.ProjectInvitation
	users       map[string]*db.User
	sandboxes   map[string]*db.Sandbox
	sbProjects  map[string]string // sandboxID -> projectID
}

func newMockProjectsStore() *mockProjectsStore {
	return &mockProjectsStore{
		projects:    make(map[string]*db.Project),
		members:     make(map[string]map[string]*db.ProjectMember),
		invitations: make(map[string]*db.ProjectInvitation),
		users:       make(map[string]*db.User),
		sandboxes:   make(map[string]*db.Sandbox),
		sbProjects:  make(map[string]string),
	}
}

func (m *mockProjectsStore) GetOrCreateDefaultProject(userID string) (*db.Project, error) {
	for _, p := range m.projects {
		if p.CreatedBy == userID && p.IsDefault {
			return p, nil
		}
	}
	p := &db.Project{
		ID:          "default-" + userID,
		Name:        "Default Project (Personal)",
		Description: "Personal private development environment",
		IsDefault:   true,
		CreatedBy:   userID,
	}
	m.projects[p.ID] = p
	if m.members[p.ID] == nil {
		m.members[p.ID] = make(map[string]*db.ProjectMember)
	}
	m.members[p.ID][userID] = &db.ProjectMember{
		ID:        "m-" + p.ID + "-" + userID,
		ProjectID: p.ID,
		UserID:    userID,
		Role:      db.ProjectMemberRoleOwner,
		Status:    db.ProjectMemberStatusActive,
	}
	return p, nil
}

func (m *mockProjectsStore) CreateProject(p *db.Project, userID string) (*db.Project, error) {
	p.ID = "proj-" + p.Name
	p.CreatedBy = userID
	p.IsDefault = false
	m.projects[p.ID] = p
	if m.members[p.ID] == nil {
		m.members[p.ID] = make(map[string]*db.ProjectMember)
	}
	m.members[p.ID][userID] = &db.ProjectMember{
		ID:        "m-" + p.ID + "-" + userID,
		ProjectID: p.ID,
		UserID:    userID,
		Role:      db.ProjectMemberRoleOwner,
		Status:    db.ProjectMemberStatusActive,
	}
	return p, nil
}

func (m *mockProjectsStore) GetProject(id string) (*db.Project, error) {
	p, ok := m.projects[id]
	if !ok {
		return nil, errors.New("project not found")
	}
	return p, nil
}

func (m *mockProjectsStore) ListUserProjects(userID string) ([]*db.Project, error) {
	_, _ = m.GetOrCreateDefaultProject(userID)
	var out []*db.Project
	for _, p := range m.projects {
		if p.CreatedBy == userID {
			out = append(out, p)
		} else if m.members[p.ID] != nil && m.members[p.ID][userID] != nil && m.members[p.ID][userID].Status == db.ProjectMemberStatusActive {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *mockProjectsStore) GetProjectRole(projectID, userID string) (db.ProjectMemberRole, error) {
	p, ok := m.projects[projectID]
	if !ok {
		return "", errors.New("project not found")
	}
	if p.CreatedBy == userID {
		return db.ProjectMemberRoleOwner, nil
	}
	if m.members[projectID] != nil && m.members[projectID][userID] != nil && m.members[projectID][userID].Status == db.ProjectMemberStatusActive {
		return m.members[projectID][userID].Role, nil
	}
	return "", errors.New("not a member")
}

func (m *mockProjectsStore) GetSandboxRole(sandboxID, userID string) (db.ProjectMemberRole, error) {
	projID, ok := m.sbProjects[sandboxID]
	if !ok {
		return "", errors.New("sandbox not linked to project")
	}
	return m.GetProjectRole(projID, userID)
}

func (m *mockProjectsStore) OwnsSandbox(sandboxID, userID string) (bool, error) {
	sb, ok := m.sandboxes[sandboxID]
	if !ok {
		return false, nil
	}
	return sb.RepositoryID == "repo-"+userID, nil
}

func (m *mockProjectsStore) GetSandbox(id string) (*db.Sandbox, error) {
	sb, ok := m.sandboxes[id]
	if !ok {
		return nil, errors.New("sandbox not found")
	}
	return sb, nil
}

func (m *mockProjectsStore) GetUser(id string) (*db.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return u, nil
}

func (m *mockProjectsStore) ListProjectMembers(projectID string) ([]*db.ProjectMember, error) {
	var out []*db.ProjectMember
	if m.members[projectID] != nil {
		for _, mem := range m.members[projectID] {
			cp := *mem
			if u, ok := m.users[mem.UserID]; ok {
				cp.User = u
			}
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *mockProjectsStore) CreateInvitation(inv *db.ProjectInvitation) error {
	p, err := m.GetProject(inv.ProjectID)
	if err != nil {
		return err
	}
	if p.IsDefault {
		return errors.New("cannot invite collaborators to a default personal project")
	}
	role, err := m.GetProjectRole(inv.ProjectID, inv.InviterID)
	if err != nil || role != db.ProjectMemberRoleOwner {
		return errors.New("access denied: only the project owner can send member invitations")
	}
	if inv.InviteeID == inv.InviterID {
		return errors.New("cannot invite yourself to a project")
	}
	if _, ok := m.users[inv.InviteeID]; !ok {
		return errors.New("recipient user not found")
	}
	if m.members[inv.ProjectID] != nil && m.members[inv.ProjectID][inv.InviteeID] != nil {
		return errors.New("user is already an active member of this project")
	}

	inv.ID = "inv-" + inv.ProjectID + "-" + inv.InviteeID
	inv.Status = db.InvitePending
	inv.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
	m.invitations[inv.ID] = inv
	return nil
}

func (m *mockProjectsStore) GetInvitation(id string) (*db.ProjectInvitation, error) {
	inv, ok := m.invitations[id]
	if !ok {
		return nil, errors.New("invitation not found")
	}
	return inv, nil
}

func (m *mockProjectsStore) ListPendingProjectInvitations(projectID string) ([]*db.ProjectInvitation, error) {
	var out []*db.ProjectInvitation
	for _, inv := range m.invitations {
		if inv.ProjectID == projectID && inv.Status == db.InvitePending {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (m *mockProjectsStore) ListUserInvitations(userID string) ([]*db.ProjectInvitation, error) {
	var out []*db.ProjectInvitation
	for _, inv := range m.invitations {
		if inv.InviteeID == userID && inv.Status == db.InvitePending {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (m *mockProjectsStore) AcceptInvitation(invitationID, userID string) error {
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	if inv.InviteeID != userID {
		return errors.New("access denied")
	}
	if inv.Status != db.InvitePending {
		return errors.New("invitation not pending")
	}
	inv.Status = db.InviteAccepted
	if m.members[inv.ProjectID] == nil {
		m.members[inv.ProjectID] = make(map[string]*db.ProjectMember)
	}
	m.members[inv.ProjectID][userID] = &db.ProjectMember{
		ID:        "m-" + inv.ProjectID + "-" + userID,
		ProjectID: inv.ProjectID,
		UserID:    userID,
		Role:      inv.Role,
		Status:    db.ProjectMemberStatusActive,
	}
	return nil
}

func (m *mockProjectsStore) RejectInvitation(invitationID, userID string) error {
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	if inv.InviteeID != userID {
		return errors.New("access denied")
	}
	inv.Status = db.InviteRejected
	return nil
}

func (m *mockProjectsStore) RevokeInvitation(invitationID, actorID string) error {
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	inv.Status = db.InviteRevoked
	return nil
}

func (m *mockProjectsStore) ResendInvitation(invitationID, actorID string) error {
	inv, ok := m.invitations[invitationID]
	if !ok {
		return errors.New("invitation not found")
	}
	inv.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
	return nil
}

func (m *mockProjectsStore) UpdateMemberRole(projectID, targetUserID string, newRole db.ProjectMemberRole, actorID string) error {
	if m.members[projectID] == nil || m.members[projectID][targetUserID] == nil {
		return errors.New("member not found")
	}
	m.members[projectID][targetUserID].Role = newRole
	return nil
}

func (m *mockProjectsStore) RemoveProjectMember(projectID, targetUserID, actorID string) error {
	if m.members[projectID] != nil {
		delete(m.members[projectID], targetUserID)
	}
	return nil
}

func TestProjectsAndCollaboration(t *testing.T) {
	mockStore := newMockProjectsStore()
	owner := &db.User{ID: "user-owner-1", Username: "owner1", Email: "owner1@coollab.io"}
	collaborator := &db.User{ID: "user-collab-2", Username: "collab2", Email: "collab2@coollab.io"}
	mockStore.users[owner.ID] = owner
	mockStore.users[collaborator.ID] = collaborator

	srv := &Server{store: mockStore}

	// 1. List Projects with authenticated owner (auto-creates default project)
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	w := httptest.NewRecorder()
	srv.handleListProjects(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var projs []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &projs)
	if len(projs) != 1 || projs[0]["isDefault"] != true {
		t.Fatalf("expected 1 default project, got %d", len(projs))
	}

	// 2. Create Shareable Project
	body := bytes.NewBufferString(`{"name":"CoolApp","description":"Shared project"}`)
	req = httptest.NewRequest("POST", "/api/projects", body)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	w = httptest.NewRecorder()
	srv.handleCreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var createdProj map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &createdProj)
	projID := createdProj["id"].(string)

	// 3. Attempt to invite to Default Project (must fail!)
	defProj, _ := mockStore.GetOrCreateDefaultProject(owner.ID)
	invBody := bytes.NewBufferString(`{"inviteeId":"user-collab-2","role":"EDITOR"}`)
	req = httptest.NewRequest("POST", "/api/projects/"+defProj.ID+"/invitations", invBody)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", defProj.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleCreateProjectInvitation(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for inviting to default project, got %d", w.Code)
	}

	// 4. Invite collaborator to Shareable Project with role VIEWER
	invBody = bytes.NewBufferString(`{"inviteeId":"user-collab-2","role":"VIEWER"}`)
	req = httptest.NewRequest("POST", "/api/projects/"+projID+"/invitations", invBody)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleCreateProjectInvitation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid invitation, got %d: %s", w.Code, w.Body.String())
	}
	var invRes map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &invRes)
	invObj := invRes["invitation"].(map[string]any)
	invID := invObj["id"].(string)

	// 5. Collaborator accepts invitation
	req = httptest.NewRequest("POST", "/api/invitations/"+invID+"/accept", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, collaborator))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", invID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleAcceptInvitation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 accepting invitation, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Verify Collaborator Role is VIEWER
	role, err := mockStore.GetProjectRole(projID, collaborator.ID)
	if err != nil || role != db.ProjectMemberRoleViewer {
		t.Fatalf("expected VIEWER role, got %s (err: %v)", role, err)
	}

	// 7. Associate a sandbox with the project
	sb := &db.Sandbox{
		ID:           "sb-test-1",
		RepositoryID: "repo-owner1",
		Subdomain:    "coolapp-dev",
		Status:       db.StatusRunning,
	}
	mockStore.sandboxes[sb.ID] = sb
	mockStore.sbProjects[sb.ID] = projID

	// 8. RBAC Test: Viewer CAN access sandbox with minRole VIEWER
	req = httptest.NewRequest("GET", "/api/sandboxes/"+sb.ID+"/files", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, collaborator))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", sb.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	_, err = srv.authorizeSandboxAccessWithRole(req, db.ProjectMemberRoleViewer)
	if err != nil {
		t.Fatalf("expected viewer to be authorized for VIEWER actions, got error: %v", err)
	}

	// 9. RBAC Test: Viewer CANNOT access sandbox with minRole EDITOR (receives forbidden error!)
	_, err = srv.authorizeSandboxAccessWithRole(req, db.ProjectMemberRoleEditor)
	if err == nil {
		t.Fatalf("expected viewer to be FORBIDDEN from EDITOR actions, but received no error")
	}

	// 10. Owner promotes Collaborator to EDITOR
	roleBody := bytes.NewBufferString(`{"role":"EDITOR"}`)
	req = httptest.NewRequest("PATCH", "/api/projects/"+projID+"/members/"+collaborator.ID, roleBody)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	rctx.URLParams.Add("userId", collaborator.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleUpdateMemberRole(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 updating member role, got %d: %s", w.Code, w.Body.String())
	}

	// 11. Now Collaborator CAN access sandbox with minRole EDITOR!
	req = httptest.NewRequest("PUT", "/api/sandboxes/"+sb.ID+"/file", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, collaborator))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", sb.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	_, err = srv.authorizeSandboxAccessWithRole(req, db.ProjectMemberRoleEditor)
	if err != nil {
		t.Fatalf("expected editor to be authorized for EDITOR actions, got error: %v", err)
	}
}
