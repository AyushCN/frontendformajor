package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/secret"
	"github.com/api-sandbox-links/backend/internal/workspacelock"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

func withURLParam(req *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

type mockEnvStore struct {
	*unifiedMockStore
	userRoles map[string]db.ProjectMemberRole
	canManage map[string]bool
}

func newMockEnvStore() *mockEnvStore {
	return &mockEnvStore{
		unifiedMockStore: newUnifiedMockStore(),
		userRoles:        make(map[string]db.ProjectMemberRole),
		canManage:        make(map[string]bool),
	}
}

func (m *mockEnvStore) GetUser(id string) (*db.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return &db.User{ID: id, Username: "test", GithubToken: []byte("token")}, nil
}

func (m *mockEnvStore) OwnsSandbox(sandboxID, userID string) (bool, error) {
	if cm, ok := m.canManage[sandboxID+":"+userID]; ok {
		return cm, nil
	}
	return m.unifiedMockStore.OwnsSandbox(sandboxID, userID)
}

func (m *mockEnvStore) CanManageSecretsForSandbox(sandboxID, userID string) (bool, error) {
	if cm, ok := m.canManage[sandboxID+":"+userID]; ok {
		return cm, nil
	}
	return m.unifiedMockStore.CanManageSecretsForSandbox(sandboxID, userID)
}

func (m *mockEnvStore) GetSandboxRole(sandboxID, userID string) (db.ProjectMemberRole, error) {
	if r, ok := m.userRoles[sandboxID+":"+userID]; ok {
		return r, nil
	}
	role, err := m.unifiedMockStore.GetSandboxRole(sandboxID, userID)
	if err == nil {
		return role, nil
	}
	return db.ProjectMemberRoleOwner, nil
}

func setupTestServer(t *testing.T) (*Server, *mockEnvStore, string) {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.NewBox(key)
	if err != nil {
		t.Fatalf("failed to create secret box: %v", err)
	}

	workDir := t.TempDir()
	store := newMockEnvStore()

	s := &Server{
		cfg: &config.Config{
			SandboxWorkDir: workDir,
		},
		store:          store,
		box:            box,
		workspaceLocks: workspacelock.NewManager(),
		logger:         slog.Default(),
	}

	return s, store, workDir
}

func TestFileTree_FlagsProtectedEnvFile(t *testing.T) {
	s, store, workDir := setupTestServer(t)
	sbID := "test-sb-1"
	subdomain := "my-subdomain"
	sbDir := filepath.Join(workDir, subdomain)
	_ = os.MkdirAll(sbDir, 0o755)

	store.sandboxes[sbID] = &db.Sandbox{
		ID:        sbID,
		Subdomain: subdomain,
	}

	// Create .env, .env.example, and main.py
	_ = os.WriteFile(filepath.Join(sbDir, ".env"), []byte("API_SECRET=123\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sbDir, ".env.example"), []byte("API_SECRET=your_secret\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sbDir, "main.py"), []byte("print('hello')\n"), 0o644)

	rootHandle, err := s.sandboxRoot(sbID)
	if err != nil {
		t.Fatalf("failed to open sandbox root: %v", err)
	}
	defer rootHandle.Close()

	entries, err := readFileTree(rootHandle)
	if err != nil {
		t.Fatalf("readFileTree failed: %v", err)
	}

	protectedMap := make(map[string]bool)
	for _, e := range entries {
		protectedMap[e.Path] = e.Protected
	}

	if !protectedMap[".env"] {
		t.Errorf("expected .env to have Protected=true, got false")
	}
	if protectedMap[".env.example"] {
		t.Errorf("expected .env.example to have Protected=false, got true")
	}
	if protectedMap["main.py"] {
		t.Errorf("expected main.py to have Protected=false, got true")
	}
}

func TestFileContent_MasksProtectedEnv(t *testing.T) {
	s, store, workDir := setupTestServer(t)
	sbID := "test-sb-2"
	subdomain := "mask-subdomain"
	sbDir := filepath.Join(workDir, subdomain)
	_ = os.MkdirAll(sbDir, 0o755)

	store.sandboxes[sbID] = &db.Sandbox{
		ID:        sbID,
		Subdomain: subdomain,
	}
	store.userRoles[sbID+":user-1"] = db.ProjectMemberRoleViewer

	// Write actual secret into .env
	secretVal := "super_secret_password_12345"
	_ = os.WriteFile(filepath.Join(sbDir, ".env"), []byte("DATABASE_URL="+secretVal+"\n"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/sandboxes/"+sbID+"/file?path=.env", nil)
	ctx := context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-1"})
	req = withURLParam(req.WithContext(ctx), "id", sbID)

	w := httptest.NewRecorder()
	s.handleFileContent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["protected"] != true {
		t.Errorf("expected protected=true in response")
	}

	content := resp["content"].(string)
	if strings.Contains(content, secretVal) {
		t.Fatalf("SECURITY VIOLATION: raw secret value was exposed in handleFileContent output: %s", content)
	}
	if !strings.Contains(content, "••••••••") {
		t.Errorf("expected masked dots in content, got: %s", content)
	}
}

func TestListEnvVars_MaskedVsRevealed(t *testing.T) {
	s, store, _ := setupTestServer(t)
	sbID := "test-sb-3"
	store.sandboxes[sbID] = &db.Sandbox{ID: sbID, Subdomain: "env-test"}

	secretVal := "my-secret-key-999"
	enc, _ := s.box.Encrypt([]byte(secretVal))
	_ = store.UpsertEnvVar(sbID, "API_KEY", enc, false)

	// 1. Viewer role without secrets permission: reveal=true is ignored, must be masked
	store.userRoles[sbID+":user-viewer"] = db.ProjectMemberRoleViewer
	store.canManage[sbID+":user-viewer"] = false

	req := httptest.NewRequest(http.MethodGet, "/api/sandboxes/"+sbID+"/env?reveal=true", nil)
	req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-viewer"})), "id", sbID)
	w := httptest.NewRecorder()

	s.handleListEnvVars(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	vars := resp["env_vars"].([]any)
	if len(vars) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(vars))
	}
	var0 := vars[0].(map[string]any)
	if var0["masked"] != true || var0["value"] != "••••••••" {
		t.Fatalf("expected viewer to get masked value, got: %+v", var0)
	}

	// 2. Owner role with secrets permission: reveal=true unmasks secret
	store.canManage[sbID+":user-owner"] = true
	reqOwner := httptest.NewRequest(http.MethodGet, "/api/sandboxes/"+sbID+"/env?reveal=true", nil)
	reqOwner = withURLParam(reqOwner.WithContext(context.WithValue(reqOwner.Context(), ctxUserKey, &db.User{ID: "user-owner"})), "id", sbID)
	wOwner := httptest.NewRecorder()

	s.handleListEnvVars(wOwner, reqOwner)
	if wOwner.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", wOwner.Code, wOwner.Body.String())
	}

	var respOwner map[string]any
	_ = json.Unmarshal(wOwner.Body.Bytes(), &respOwner)
	varsOwner := respOwner["env_vars"].([]any)
	varOwner0 := varsOwner[0].(map[string]any)
	if varOwner0["masked"] != false || varOwner0["value"] != secretVal {
		t.Fatalf("expected owner to get decrypted secret value, got: %+v", varOwner0)
	}
}

func TestUploadEnv_MergeAndReplaceStrategies(t *testing.T) {
	s, store, _ := setupTestServer(t)
	sbID := "test-sb-4"
	store.sandboxes[sbID] = &db.Sandbox{ID: sbID, Subdomain: "upload-test"}
	store.canManage[sbID+":user-1"] = true

	// Pre-seed an existing variable
	encOld, _ := s.box.Encrypt([]byte("old_value"))
	_ = store.UpsertEnvVar(sbID, "EXISTING_KEY", encOld, false)

	// 1. Upload with strategy="merge": existing key must NOT be overwritten
	uploadBody := `{
		"content": "EXISTING_KEY=new_overwritten_val\nNEW_KEY=new_val\n",
		"strategy": "merge"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/sandboxes/"+sbID+"/env/upload", strings.NewReader(uploadBody))
	req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	w := httptest.NewRecorder()

	s.handleUploadEnv(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	existingVar, _ := store.GetEnvVar(sbID, "EXISTING_KEY")
	plainExisting, _ := s.box.Decrypt(existingVar.Value)
	if string(plainExisting) != "old_value" {
		t.Fatalf("merge strategy failed: existing key was overwritten with %q", string(plainExisting))
	}

	newVar, _ := store.GetEnvVar(sbID, "NEW_KEY")
	plainNew, _ := s.box.Decrypt(newVar.Value)
	if string(plainNew) != "new_val" {
		t.Fatalf("merge strategy failed: new key was not inserted: %q", string(plainNew))
	}

	// 2. Upload with strategy="replace": existing keys are replaced
	uploadReplace := `{
		"content": "REPLACED_KEY=all_fresh\n",
		"strategy": "replace"
	}`
	reqReplace := httptest.NewRequest(http.MethodPost, "/api/sandboxes/"+sbID+"/env/upload", strings.NewReader(uploadReplace))
	reqReplace = withURLParam(reqReplace.WithContext(context.WithValue(reqReplace.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	wReplace := httptest.NewRecorder()

	s.handleUploadEnv(wReplace, reqReplace)
	if wReplace.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on replace, got %d: %s", wReplace.Code, wReplace.Body.String())
	}

	vars, _ := store.ListEnvVars(sbID)
	if len(vars) != 1 || vars[0].Key != "REPLACED_KEY" {
		t.Fatalf("replace strategy failed: expected only REPLACED_KEY, got: %+v", vars)
	}
}

func TestSetAndDeleteEnvVar(t *testing.T) {
	s, store, _ := setupTestServer(t)
	sbID := "test-sb-5"
	store.sandboxes[sbID] = &db.Sandbox{ID: sbID, Subdomain: "crud-test"}
	store.canManage[sbID+":user-1"] = true

	// 1. Create single variable
	body := `{"key": "NEW_TOKEN", "value": "secret_abc_123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/sandboxes/"+sbID+"/env", strings.NewReader(body))
	req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	w := httptest.NewRecorder()

	s.handleSetEnvVar(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	saved, err := store.GetEnvVar(sbID, "NEW_TOKEN")
	if err != nil {
		t.Fatalf("failed to retrieve saved var: %v", err)
	}
	dec, _ := s.box.Decrypt(saved.Value)
	if string(dec) != "secret_abc_123" {
		t.Fatalf("expected decrypted value 'secret_abc_123', got %q", string(dec))
	}

	// 2. Delete single variable
	delReq := httptest.NewRequest(http.MethodDelete, "/api/sandboxes/"+sbID+"/env/NEW_TOKEN", nil)
	delReq = withURLParam(delReq.WithContext(context.WithValue(delReq.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	delRctx := chi.RouteContext(delReq.Context())
	delRctx.URLParams.Add("key", "NEW_TOKEN")
	delW := httptest.NewRecorder()

	s.handleDeleteEnvVar(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got %d: %s", delW.Code, delW.Body.String())
	}

	_, err = store.GetEnvVar(sbID, "NEW_TOKEN")
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("expected ErrRecordNotFound after delete, got %v", err)
	}
}

func TestDetectEnvRequirements(t *testing.T) {
	s, store, workDir := setupTestServer(t)
	sbID := "test-sb-6"
	subdomain := "req-subdomain"
	sbDir := filepath.Join(workDir, subdomain)
	_ = os.MkdirAll(sbDir, 0o755)

	store.sandboxes[sbID] = &db.Sandbox{
		ID:        sbID,
		Subdomain: subdomain,
	}

	// Create .env.example with two required keys
	exampleContent := "PORT=8080\nDATABASE_URL=postgres://...\nOPTIONAL_VAR=\n"
	_ = os.WriteFile(filepath.Join(sbDir, ".env.example"), []byte(exampleContent), 0o644)

	// Pre-configure one of them in DB
	enc, _ := s.box.Encrypt([]byte("postgres://real_db"))
	_ = store.UpsertEnvVar(sbID, "DATABASE_URL", enc, false)

	req := httptest.NewRequest(http.MethodGet, "/api/sandboxes/"+sbID+"/env/requirements", nil)
	req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	w := httptest.NewRecorder()

	s.handleDetectEnvRequirements(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	reqs := resp["required"].([]any)
	if len(reqs) != 3 {
		t.Fatalf("expected 3 detected vars, got %d", len(reqs))
	}

	cfgMap := make(map[string]bool)
	for _, item := range reqs {
		m := item.(map[string]any)
		cfgMap[m["key"].(string)] = m["configured"].(bool)
	}

	if !cfgMap["DATABASE_URL"] {
		t.Errorf("expected DATABASE_URL to be configured=true")
	}
	if cfgMap["PORT"] {
		t.Errorf("expected PORT to be configured=false")
	}
}

func TestSaveFile_ProtectedEnvSyncsToStore(t *testing.T) {
	s, store, workDir := setupTestServer(t)
	sbID := "test-sb-7"
	subdomain := "sync-subdomain"
	sbDir := filepath.Join(workDir, subdomain)
	_ = os.MkdirAll(sbDir, 0o755)

	store.sandboxes[sbID] = &db.Sandbox{
		ID:        sbID,
		Subdomain: subdomain,
	}
	store.canManage[sbID+":user-1"] = true

	// Save .env file via handleSaveFile
	body := `{"path": ".env", "content": "SYNCED_SECRET=top_secret_value\nAPP_MODE=production\n"}`
	req := httptest.NewRequest(http.MethodPut, "/api/sandboxes/"+sbID+"/file", strings.NewReader(body))
	req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, &db.User{ID: "user-1"})), "id", sbID)
	w := httptest.NewRecorder()

	s.handleSaveFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// Verify values synced to store encrypted
	v1, err := store.GetEnvVar(sbID, "SYNCED_SECRET")
	if err != nil {
		t.Fatalf("expected SYNCED_SECRET to be synced to store, got error: %v", err)
	}
	dec1, _ := s.box.Decrypt(v1.Value)
	if string(dec1) != "top_secret_value" {
		t.Fatalf("expected decrypted value 'top_secret_value', got %q", string(dec1))
	}

	v2, err := store.GetEnvVar(sbID, "APP_MODE")
	if err != nil {
		t.Fatalf("expected APP_MODE to be synced to store, got error: %v", err)
	}
	dec2, _ := s.box.Decrypt(v2.Value)
	if string(dec2) != "production" {
		t.Fatalf("expected decrypted value 'production', got %q", string(dec2))
	}
}
