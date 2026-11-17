package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/secret"
	"github.com/google/uuid"
)

func validTestEncryptionKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

type mockAuthStore struct {
	Store
	users map[int64]*db.User
}

func newMockAuthStore() *mockAuthStore {
	return &mockAuthStore{users: make(map[int64]*db.User)}
}

func (m *mockAuthStore) GetUserByGithubID(githubID int64) (*db.User, error) {
	u, ok := m.users[githubID]
	if !ok {
		return nil, errors.New("user not found")
	}
	return u, nil
}

func (m *mockAuthStore) UpsertUser(u *db.User) error {
	m.users[u.GithubID] = u
	return nil
}

func (m *mockAuthStore) GetOrCreateDefaultProject(ownerID string) (*db.Project, error) {
	return &db.Project{ID: "default-project", Name: "Default Project", CreatedBy: ownerID, IsDefault: true}, nil
}

func (m *mockAuthStore) SearchUsers(query string, limit int) ([]*db.User, error) {
	var results []*db.User
	for _, u := range m.users {
		if strings.Contains(strings.ToLower(u.Username), strings.ToLower(query)) || strings.Contains(strings.ToLower(u.Email), strings.ToLower(query)) {
			results = append(results, u)
		}
	}
	return results, nil
}

func TestProductionOAuthConfigurationEnforcement(t *testing.T) {
	keyBase64 := base64.StdEncoding.EncodeToString(validTestEncryptionKey())
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/testdb")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("SECRETS_ENCRYPTION_KEY", keyBase64)
	t.Setenv("ENV", "production")
	t.Setenv("GITHUB_CLIENT_ID", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected config.Load() to fail in production when GITHUB_CLIENT_ID is missing, got nil")
	}
	if !strings.Contains(err.Error(), "required in production") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Supply valid production credentials
	t.Setenv("GITHUB_CLIENT_ID", "gh-client-id")
	t.Setenv("GITHUB_CLIENT_SECRET", "gh-client-secret")
	t.Setenv("GITHUB_OAUTH_CALLBACK_URL", "https://example.com/api/auth/github/callback")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected config.Load() to succeed with full production OAuth, got: %v", err)
	}
	if cfg.DevAuth {
		t.Fatalf("expected DevAuth to be strictly false in production, got true")
	}
}

func TestOAuthCSRFValidation(t *testing.T) {
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockAuthStore()
	cfg := &config.Config{
		GitHubClientID:         "client-123",
		GitHubClientSecret:     "secret-123",
		GitHubOAuthCallbackURL: "http://localhost:8080/callback",
		FrontendURL:            "http://localhost:3000",
		EncryptionKey:          validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	// 1. Auth start sets CSRF cookie and redirects
	req := httptest.NewRequest("GET", "/api/auth/github", nil)
	w := httptest.NewRecorder()
	srv.handleAuthStart(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected status 302, got %d", resp.StatusCode)
	}
	var csrfCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == oauthCsrfCookie {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatal("expected csrf state cookie to be set")
	}

	// 2. Callback with mismatched state fails
	cbReq := httptest.NewRequest("GET", "/api/auth/github/callback?state=forged-state&code=test-code", nil)
	cbReq.AddCookie(csrfCookie)
	cbW := httptest.NewRecorder()
	srv.handleAuthCallback(cbW, cbReq)
	if cbW.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for CSRF state mismatch, got %d", cbW.Code)
	}

	// 3. Callback with missing cookie fails
	cbReq2 := httptest.NewRequest("GET", "/api/auth/github/callback?state="+csrfCookie.Value+"&code=test-code", nil)
	cbW2 := httptest.NewRecorder()
	srv.handleAuthCallback(cbW2, cbReq2)
	if cbW2.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for missing CSRF cookie, got %d", cbW2.Code)
	}
}

func TestSessionIssuanceExpirationAndRevocation(t *testing.T) {
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockAuthStore()
	user := &db.User{
		ID:        "user-prod-1",
		GithubID:  998877,
		Username:  "production-user",
		Email:     "prod@example.com",
		AvatarURL: "https://avatar.example.com/1",
	}
	_ = store.UpsertUser(user)

	cfg := &config.Config{
		FrontendURL:   "http://localhost:3000",
		EncryptionKey: validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	// 1. Valid Session Issuance
	nonce := uuid.NewString()
	validPayload := fmt.Sprintf("%d:%d:%s", user.GithubID, time.Now().Unix(), nonce)
	encValid, _ := box.Encrypt([]byte(validPayload))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: string(encValid)})
	resolved, err := srv.sessionUser(req)
	if err != nil || resolved == nil {
		t.Fatalf("expected valid session to authenticate, got error: %v", err)
	}
	if resolved.Username != "production-user" {
		t.Fatalf("expected username 'production-user', got %s", resolved.Username)
	}

	// 2. Expired Session (>30 days)
	expiredTime := time.Now().Add(-31 * 24 * time.Hour).Unix()
	expiredPayload := fmt.Sprintf("%d:%d:%s", user.GithubID, expiredTime, uuid.NewString())
	encExpired, _ := box.Encrypt([]byte(expiredPayload))

	reqExp := httptest.NewRequest("GET", "/api/me", nil)
	reqExp.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: string(encExpired)})
	_, expErr := srv.sessionUser(reqExp)
	if expErr == nil || !strings.Contains(expErr.Error(), "session expired") {
		t.Fatalf("expected session expired error, got: %v", expErr)
	}

	// 3. Logout clears cookie
	logoutReq := httptest.NewRequest("POST", "/api/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: string(encValid)})
	logoutW := httptest.NewRecorder()
	srv.handleLogout(logoutW, logoutReq)
	if logoutW.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on logout, got %d", logoutW.Code)
	}
	var clearedCookie *http.Cookie
	for _, c := range logoutW.Result().Cookies() {
		if c.Name == oauthStateCookie {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Fatalf("expected session cookie to have MaxAge -1, got: %+v", clearedCookie)
	}
}

func TestCleanIdentityContract(t *testing.T) {
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockAuthStore()
	user := &db.User{
		ID:          "user-prod-2",
		GithubID:    112233,
		Username:    "clean-identity",
		Email:       "identity@example.com",
		AvatarURL:   "https://example.com/avatar.png",
		GithubToken: []byte("encrypted-secret-token"),
	}
	_ = store.UpsertUser(user)

	cfg := &config.Config{
		FrontendURL:   "http://localhost:3000",
		EncryptionKey: validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	nonce := uuid.NewString()
	payload := fmt.Sprintf("%d:%d:%s", user.GithubID, time.Now().Unix(), nonce)
	enc, _ := box.Encrypt([]byte(payload))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: string(enc)})
	w := httptest.NewRecorder()

	srv.requireUser(http.HandlerFunc(srv.handleMe)).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to decode me response: %v", err)
	}

	if data["id"] != "user-prod-2" || data["username"] != "clean-identity" || data["email"] != "identity@example.com" {
		t.Fatalf("unexpected user fields in /api/me: %+v", data)
	}
	// Verify zero credential leakage
	if _, ok := data["token"]; ok {
		t.Fatal("security violation: raw token leaked in /api/me")
	}
	if _, ok := data["github_token"]; ok {
		t.Fatal("security violation: github_token leaked in /api/me")
	}
}

func TestDevLoginDisabledInProduction(t *testing.T) {
	box, _ := secret.NewBox(validTestEncryptionKey())
	store := newMockAuthStore()
	cfg := &config.Config{
		Environment:   "production",
		DevAuth:       false,
		EncryptionKey: validTestEncryptionKey(),
	}
	srv := New(cfg, store, nil, nil, nil, box, nil, nil)

	req := httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"username":"hacker"}`))
	w := httptest.NewRecorder()
	srv.handleDevLogin(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for dev-login in production, got %d", w.Code)
	}
}
