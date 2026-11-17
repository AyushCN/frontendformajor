package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/api-sandbox-links/backend/internal/db"
)

// mockStoreForUserSearch implements Store interface for user search testing
type mockStoreForUserSearch struct {
	Store
	users []*db.User
}

func (m *mockStoreForUserSearch) SearchUsers(query string, limit int) ([]*db.User, error) {
	var matched []*db.User
	for _, u := range m.users {
		matched = append(matched, u)
	}
	return matched, nil
}

func TestHandleSearchUsers(t *testing.T) {
	mockStore := &mockStoreForUserSearch{
		users: []*db.User{
			{
				ID:        "user-1",
				Username:  "alexr",
				Email:     "alex@coollab.io",
				AvatarURL: "https://avatar.url/alex",
			},
		},
	}

	srv := &Server{
		store: mockStore,
	}

	// 1. Query too short (< 2 chars)
	req := httptest.NewRequest("GET", "/api/users/search?q=a", nil)
	w := httptest.NewRecorder()
	srv.handleSearchUsers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var res1 map[string][]any
	_ = json.Unmarshal(w.Body.Bytes(), &res1)
	if len(res1["users"]) != 0 {
		t.Fatalf("expected empty results for short query, got %d", len(res1["users"]))
	}

	// 2. Valid query
	req2 := httptest.NewRequest("GET", "/api/users/search?q=alex", nil)
	w2 := httptest.NewRecorder()
	srv.handleSearchUsers(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	var res2 map[string][]map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if len(res2["users"]) != 1 {
		t.Fatalf("expected 1 user, got %d", len(res2["users"]))
	}
	user := res2["users"][0]
	if user["username"] != "alexr" {
		t.Fatalf("expected username alexr, got %v", user["username"])
	}
	if user["email"] != "alex@coollab.io" {
		t.Fatalf("expected email alex@coollab.io, got %v", user["email"])
	}
}
