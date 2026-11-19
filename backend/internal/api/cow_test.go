package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/secret"
	"github.com/go-chi/chi/v5"
)

type mockCoWStore struct {
	*unifiedMockStore
	ownership    map[string]string                          // sandboxID -> userID
	sandboxRoles map[string]map[string]db.ProjectMemberRole // sandboxID -> userID -> role
}

func newMockCoWStore() *mockCoWStore {
	return &mockCoWStore{
		unifiedMockStore: newUnifiedMockStore(),
		ownership:        make(map[string]string),
		sandboxRoles:     make(map[string]map[string]db.ProjectMemberRole),
	}
}

func (m *mockCoWStore) OwnsSandbox(sandboxID, userID string) (bool, error) {
	if owner, ok := m.ownership[sandboxID]; ok {
		return owner == userID, nil
	}
	return m.unifiedMockStore.OwnsSandbox(sandboxID, userID)
}

func (m *mockCoWStore) GetSandboxRole(sandboxID, userID string) (db.ProjectMemberRole, error) {
	if roles, ok := m.sandboxRoles[sandboxID]; ok {
		if r, has := roles[userID]; has {
			return r, nil
		}
	}
	return m.unifiedMockStore.GetSandboxRole(sandboxID, userID)
}

func (m *mockCoWStore) CreateSandboxWithinLimits(sb *db.Sandbox, ownerUserID string, maxPerUser, maxTotal int) error {
	m.ownership[sb.ID] = ownerUserID
	if _, ok := m.sandboxRoles[sb.ID]; !ok {
		m.sandboxRoles[sb.ID] = make(map[string]db.ProjectMemberRole)
	}
	m.sandboxRoles[sb.ID][ownerUserID] = db.ProjectMemberRoleOwner
	return m.unifiedMockStore.CreateSandboxWithinLimits(sb, ownerUserID, maxPerUser, maxTotal)
}

func (m *mockCoWStore) CreateSandboxWithProject(sb *db.Sandbox, ownerUserID, projectID string, maxPerUser, maxTotal int) error {
	m.ownership[sb.ID] = ownerUserID
	if _, ok := m.sandboxRoles[sb.ID]; !ok {
		m.sandboxRoles[sb.ID] = make(map[string]db.ProjectMemberRole)
	}
	m.sandboxRoles[sb.ID][ownerUserID] = db.ProjectMemberRoleOwner
	return m.unifiedMockStore.CreateSandboxWithProject(sb, ownerUserID, projectID, maxPerUser, maxTotal)
}

func TestEditorModifySharedSandboxTriggersCopyOnWrite(t *testing.T) {
	workDir := t.TempDir()
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockCoWStore()

	ownerUser := &db.User{ID: "user-owner", Username: "owner", Email: "owner@test.com"}
	editorUser := &db.User{ID: "user-editor", Username: "editor", Email: "editor@test.com"}
	viewerUser := &db.User{ID: "user-viewer", Username: "viewer", Email: "viewer@test.com"}

	// Set up original parent sandbox owned by ownerUser
	parentSB := &db.Sandbox{
		ID:                 "parent-sb-1",
		Subdomain:          "parent-sub",
		Status:             db.StatusRunning,
		IdleTimeoutSeconds: 900,
		LifetimeSeconds:    7200,
		ExpiresAt:          time.Now().Add(2 * time.Hour),
		CreatedAt:          time.Now(),
	}
	_ = store.CreateSandboxWithinLimits(parentSB, ownerUser.ID, 10, 10)
	_ = store.AssociateProjectSandbox("proj-1", parentSB.ID)

	// Roles in project: editorUser has EDITOR, viewerUser has VIEWER
	store.sandboxRoles[parentSB.ID][editorUser.ID] = db.ProjectMemberRoleEditor
	store.sandboxRoles[parentSB.ID][viewerUser.ID] = db.ProjectMemberRoleViewer

	// Create test file in parent sandbox directory
	parentDirPath := filepath.Join(workDir, parentSB.Subdomain)
	_ = os.MkdirAll(parentDirPath, 0o755)
	_ = os.WriteFile(filepath.Join(parentDirPath, "main.py"), []byte("print('original parent code')\n"), 0o644)

	cfg := &config.Config{
		SandboxWorkDir: workDir,
		EncryptionKey:  validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	// 1. Viewer attempt to mutate -> rejected with 403 Forbidden
	viewerReq := httptest.NewRequest("PUT", "/api/sandboxes/parent-sb-1/file", strings.NewReader(`{"path":"main.py","content":"hacked"}`))
	viewerCtx := context.WithValue(viewerReq.Context(), ctxUserKey, viewerUser)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", parentSB.ID)
	viewerCtx = context.WithValue(viewerCtx, chi.RouteCtxKey, rctx)
	viewerW := httptest.NewRecorder()

	srv.requireSandboxRoleForMutation(viewerW, viewerReq.WithContext(viewerCtx))
	if viewerW.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for Viewer mutation, got %d", viewerW.Code)
	}

	// Verify parent file is still original
	data, _ := os.ReadFile(filepath.Join(parentDirPath, "main.py"))
	if string(data) != "print('original parent code')\n" {
		t.Fatalf("parent sandbox was modified by viewer attempt!")
	}

	// 2. Editor attempt to mutate -> triggers automatic Copy-On-Write!
	editorReq := httptest.NewRequest("PUT", "/api/sandboxes/parent-sb-1/file", strings.NewReader(`{"path":"main.py","content":"print('editor update')\n"}`))
	editorCtx := context.WithValue(editorReq.Context(), ctxUserKey, editorUser)
	editorCtx = context.WithValue(editorCtx, chi.RouteCtxKey, rctx)
	editorW := httptest.NewRecorder()

	mutatedSB, ok := srv.requireSandboxRoleForMutation(editorW, editorReq.WithContext(editorCtx))
	if !ok || mutatedSB == nil {
		t.Fatalf("expected requireSandboxRoleForMutation to succeed for Editor with CoW, got ok=false")
	}

	// Check CoW response headers
	forkedIDHeader := editorW.Header().Get("X-CoW-Forked-Sandbox-ID")
	if forkedIDHeader == "" || forkedIDHeader != mutatedSB.ID {
		t.Fatalf("expected header X-CoW-Forked-Sandbox-ID to equal %s, got %s", mutatedSB.ID, forkedIDHeader)
	}

	// The returned sandbox MUST be the forked child sandbox, NOT the parent!
	if mutatedSB.ID == parentSB.ID {
		t.Fatalf("Copy-on-Write violation: returned parent sandbox ID instead of child fork!")
	}
	if mutatedSB.ParentSandboxID == nil || *mutatedSB.ParentSandboxID != parentSB.ID {
		t.Fatalf("expected child ParentSandboxID to equal parent ID, got %v", mutatedSB.ParentSandboxID)
	}

	// The child sandbox must be owned by the editor
	if store.ownership[mutatedSB.ID] != editorUser.ID {
		t.Fatalf("expected child fork to be owned by editor, got %s", store.ownership[mutatedSB.ID])
	}

	// The child must be associated with the project
	if store.projectSandboxes[mutatedSB.ID] != "proj-1" {
		t.Fatalf("expected child fork to be associated with project proj-1, got %s", store.projectSandboxes[mutatedSB.ID])
	}

	// The parent workspace on disk must be completely untouched!
	parentData, _ := os.ReadFile(filepath.Join(parentDirPath, "main.py"))
	if string(parentData) != "print('original parent code')\n" {
		t.Fatalf("CRITICAL REGRESSION: original parent sandbox file was modified! Content: %s", string(parentData))
	}

	// The child workspace must exist on disk and have a copy of the parent files
	childDirPath := filepath.Join(workDir, mutatedSB.Subdomain)
	childData, err := os.ReadFile(filepath.Join(childDirPath, "main.py"))
	if err != nil {
		t.Fatalf("failed to read child sandbox directory: %v", err)
	}
	if string(childData) != "print('original parent code')\n" {
		t.Fatalf("child copy directory does not contain initial parent file contents!")
	}
}

func TestOwnerDirectMutationNoFork(t *testing.T) {
	workDir := t.TempDir()
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockCoWStore()

	ownerUser := &db.User{ID: "user-owner-2", Username: "owner2", Email: "owner2@test.com"}
	parentSB := &db.Sandbox{
		ID:                 "owner-sb-1",
		Subdomain:          "owner-sub",
		Status:             db.StatusRunning,
		IdleTimeoutSeconds: 900,
		LifetimeSeconds:    7200,
		ExpiresAt:          time.Now().Add(2 * time.Hour),
		CreatedAt:          time.Now(),
	}
	_ = store.CreateSandboxWithinLimits(parentSB, ownerUser.ID, 10, 10)

	cfg := &config.Config{
		SandboxWorkDir: workDir,
		EncryptionKey:  validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	ownerReq := httptest.NewRequest("PUT", "/api/sandboxes/owner-sb-1/file", nil)
	ownerCtx := context.WithValue(ownerReq.Context(), ctxUserKey, ownerUser)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", parentSB.ID)
	ownerCtx = context.WithValue(ownerCtx, chi.RouteCtxKey, rctx)
	ownerW := httptest.NewRecorder()

	mutatedSB, ok := srv.requireSandboxRoleForMutation(ownerW, ownerReq.WithContext(ownerCtx))
	if !ok || mutatedSB == nil {
		t.Fatalf("expected requireSandboxRoleForMutation to succeed for direct owner")
	}

	// Owner directly modifies their own sandbox; NO fork is created
	if mutatedSB.ID != parentSB.ID {
		t.Fatalf("expected direct owner to modify original sandbox ID %s, got %s", parentSB.ID, mutatedSB.ID)
	}
	if ownerW.Header().Get("X-CoW-Forked-Sandbox-ID") != "" {
		t.Fatalf("owner mutation should not set X-CoW-Forked-Sandbox-ID header")
	}
}
