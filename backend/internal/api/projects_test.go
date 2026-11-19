package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/go-chi/chi/v5"
)

func TestProjectsAndCollaboration(t *testing.T) {
	mockStore := newMockProjectsStore()
	owner := &db.User{ID: "user-owner-1", Username: "owner1", Email: "owner1@coollab.io"}
	collaborator := &db.User{ID: "user-collab-2", Username: "collab2", Email: "collab2@coollab.io"}
	mockStore.users[owner.ID] = owner
	mockStore.users[collaborator.ID] = collaborator

	srv := &Server{store: mockStore, logger: slog.Default()}

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

	// 12. Create a second project to test Transfer Sandbox
	destProj := mockStore.seedProject("proj-dest-1", "Destination Project", owner.ID, false)

	// 13. Owner transfers sandbox sb.ID to destProj
	transferBody := bytes.NewBufferString(`{"projectId":"` + destProj.ID + `"}`)
	req = httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/transfer", transferBody)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", sb.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleTransferSandbox(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 transferring sandbox, got %d: %s", w.Code, w.Body.String())
	}
	if mockStore.sbProjects[sb.ID] != destProj.ID {
		t.Fatalf("expected sandbox project association to be %s, got %s", destProj.ID, mockStore.sbProjects[sb.ID])
	}
}

