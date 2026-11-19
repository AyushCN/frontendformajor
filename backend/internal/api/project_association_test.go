package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestProjectAssociation_CreateSandboxWithIntegrity(t *testing.T) {
	mockStore := newMockProjectsStore()
	user := &db.User{ID: "user-test-1", Username: "tester", Email: "tester@example.com"}
	mockStore.users[user.ID] = user

	// Setup a project where user is Editor
	sharedProj := &db.Project{
		ID:        "proj-shared-123",
		Name:      "Shared Project",
		CreatedBy: "other-user",
	}
	mockStore.projects[sharedProj.ID] = sharedProj
	mockStore.members[sharedProj.ID] = map[string]*db.ProjectMember{
		user.ID: {
			ID:        "m-1",
			ProjectID: sharedProj.ID,
			UserID:    user.ID,
			Role:      db.ProjectMemberRoleEditor,
			Status:    db.ProjectMemberStatusActive,
		},
	}

	srv := &Server{store: mockStore}

	// 1. Success creating in shared project
	body, _ := json.Marshal(map[string]any{
		"repo_url":   "https://github.com/org/service",
		"project_id": sharedProj.ID,
		"name":       "My Shared Service",
	})
	req := httptest.NewRequest("POST", "/api/sandboxes", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, user))
	w := httptest.NewRecorder()
	srv.handleCreateSandbox(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["project_id"] != sharedProj.ID || res["projectId"] != sharedProj.ID {
		t.Fatalf("expected project_id to be %q, got project_id=%v, projectId=%v", sharedProj.ID, res["project_id"], res["projectId"])
	}

	// 2. Reject conflicting project_id and projectId
	conflictBody, _ := json.Marshal(map[string]any{
		"repo_url":   "https://github.com/org/service",
		"project_id": "proj-a",
		"projectId":  "proj-b",
	})
	req = httptest.NewRequest("POST", "/api/sandboxes", bytes.NewReader(conflictBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, user))
	w = httptest.NewRecorder()
	srv.handleCreateSandbox(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting project IDs, got %d", w.Code)
	}

	// 3. Reject non-existent project
	nonExistentBody, _ := json.Marshal(map[string]any{
		"repo_url":   "https://github.com/org/service",
		"project_id": "non-existent-proj",
	})
	req = httptest.NewRequest("POST", "/api/sandboxes", bytes.NewReader(nonExistentBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, user))
	w = httptest.NewRecorder()
	srv.handleCreateSandbox(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent project, got %d", w.Code)
	}
}

func TestProjectAssociation_SerializationNoFallbackGuessing(t *testing.T) {
	mockStore := newMockProjectsStore()
	srv := &Server{store: mockStore}

	unassociatedSB := &db.Sandbox{
		ID:           uuid.NewString(),
		Name:         "Legacy Sandbox",
		Slug:         "legacy-sandbox",
		Subdomain:    "legacy-123456",
		RepositoryID: "repo-someone-else",
		CreatedAt:    time.Now().UTC(),
	}

	data := srv.sandboxJSON(unassociatedSB)
	if data["project_id"] != "" || data["projectId"] != "" {
		t.Fatalf("expected empty project_id for unassociated sandbox, got %v", data["project_id"])
	}
}

func TestProjectAssociation_TransferSandbox(t *testing.T) {
	mockStore := newMockProjectsStore()
	owner := &db.User{ID: "user-owner-1", Username: "owner", Email: "owner@example.com"}
	mockStore.users[owner.ID] = owner

	projA := &db.Project{ID: "proj-alpha", Name: "Project Alpha", CreatedBy: owner.ID}
	projB := &db.Project{ID: "proj-beta", Name: "Project Beta", CreatedBy: owner.ID}
	mockStore.projects[projA.ID] = projA
	mockStore.projects[projB.ID] = projB
	mockStore.members[projA.ID] = map[string]*db.ProjectMember{
		owner.ID: {ProjectID: projA.ID, UserID: owner.ID, Role: db.ProjectMemberRoleOwner, Status: db.ProjectMemberStatusActive},
	}
	mockStore.members[projB.ID] = map[string]*db.ProjectMember{
		owner.ID: {ProjectID: projB.ID, UserID: owner.ID, Role: db.ProjectMemberRoleOwner, Status: db.ProjectMemberStatusActive},
	}

	sb := &db.Sandbox{
		ID:           "sb-transfer-1",
		Name:         "Transfer Candidate",
		Slug:         "transfer-candidate",
		Subdomain:    "tc-123456",
		RepositoryID: "repo-1",
		Status:       db.StatusRunning,
	}
	mockStore.sandboxes[sb.ID] = sb
	mockStore.sbProjects[sb.ID] = projA.ID

	srv := &Server{store: mockStore}

	// 1. Successful transfer to Project Beta
	body, _ := json.Marshal(map[string]any{"project_id": projB.ID})
	req := httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/transfer", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	// Set chi URL param
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sb.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	srv.handleTransferSandbox(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["project_id"] != projB.ID || res["projectId"] != projB.ID {
		t.Fatalf("expected transfer response project_id %q, got %v", projB.ID, res["project_id"])
	}

	// 2. Reject conflicting IDs in transfer
	conflictBody, _ := json.Marshal(map[string]any{"project_id": "proj-a", "projectId": "proj-b"})
	req = httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/transfer", bytes.NewReader(conflictBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey, owner))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleTransferSandbox(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting transfer project IDs, got %d", w.Code)
	}
}
