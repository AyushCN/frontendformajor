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

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard display name",
			input:    "Weather API Development",
			expected: "weather-api-development",
		},
		{
			name:     "with special characters and symbols",
			input:    "My App! @v2 #prod $test %^&*()",
			expected: "my-app-v2-prod-test",
		},
		{
			name:     "multiple consecutive hyphens and spaces",
			input:    "  hello---world   test--123  ",
			expected: "hello-world-test-123",
		},
		{
			name:     "length capped at 40 characters",
			input:    "this is an extremely long sandbox display name that exceeds forty characters by far",
			expected: "this-is-an-extremely-long-sandbox-displa",
		},
		{
			name:     "empty or symbol-only string falls back to sandbox",
			input:    "   !!!   ",
			expected: "sandbox",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := db.NormalizeSlug(tc.input)
			if actual != tc.expected {
				t.Errorf("NormalizeSlug(%q) = %q; expected %q", tc.input, actual, tc.expected)
			}
			if len(actual) > 40 {
				t.Errorf("NormalizeSlug(%q) produced slug exceeding 40 chars: len=%d", tc.input, len(actual))
			}
		})
	}
}

func TestUniqueSubdomain(t *testing.T) {
	s := &Server{}
	sub, err := s.uniqueSubdomain("weather-api", "a81f2c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "weather-api-a81f2c"
	if sub != expected {
		t.Errorf("uniqueSubdomain() = %q; expected %q", sub, expected)
	}

	subEmpty, err := s.uniqueSubdomain("", "b92e1d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedEmpty := "sandbox-b92e1d"
	if subEmpty != expectedEmpty {
		t.Errorf("uniqueSubdomain() with empty slug = %q; expected %q", subEmpty, expectedEmpty)
	}
}

func TestHandleRenameSandbox(t *testing.T) {
	store := newMockProjectsStore()
	ownerUser := &db.User{ID: "user-owner", Email: "owner@example.com"}
	viewerUser := &db.User{ID: "user-viewer", Email: "viewer@example.com"}
	store.users[ownerUser.ID] = ownerUser
	store.users[viewerUser.ID] = viewerUser

	proj := &db.Project{
		ID:        "proj-rename-1",
		Name:      "Project Naming",
		CreatedBy: ownerUser.ID,
	}
	store.projects[proj.ID] = proj
	store.members[proj.ID] = map[string]*db.ProjectMember{
		ownerUser.ID:  {ID: "m-owner", ProjectID: proj.ID, UserID: ownerUser.ID, Role: db.ProjectMemberRoleOwner, Status: db.ProjectMemberStatusActive},
		viewerUser.ID: {ID: "m-viewer", ProjectID: proj.ID, UserID: viewerUser.ID, Role: db.ProjectMemberRoleViewer, Status: db.ProjectMemberStatusActive},
	}

	origSubdomain := "old-sandbox-123456"
	sb := &db.Sandbox{
		ID:           "sb-rename-test",
		Name:         "Old Sandbox",
		Slug:         "old-sandbox",
		Subdomain:    origSubdomain,
		RepositoryID: "repo-" + ownerUser.ID,
		Status:       db.StatusRunning,
	}
	store.sandboxes[sb.ID] = sb
	store.sbProjects[sb.ID] = proj.ID

	s := &Server{store: store, logger: slog.Default()}
	r := chi.NewRouter()
	r.Post("/api/sandboxes/{id}/rename", s.handleRenameSandbox)

	t.Run("owner can rename sandbox successfully", func(t *testing.T) {
		body := map[string]string{"name": "Weather API Development"}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/rename", bytes.NewReader(bodyBytes))
		req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, ownerUser)), "id", sb.ID)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response JSON: %v", err)
		}

		sbResp, ok := resp["sandbox"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected 'sandbox' object in response, got %v", resp)
		}

		if sbResp["name"] != "Weather API Development" {
			t.Errorf("expected name %q, got %v", "Weather API Development", sbResp["name"])
		}
		if sbResp["slug"] != "weather-api-development" {
			t.Errorf("expected slug %q, got %v", "weather-api-development", sbResp["slug"])
		}

		// Verify store state updated
		updated := store.sandboxes[sb.ID]
		if updated.Name != "Weather API Development" {
			t.Errorf("store sandbox Name not updated: %q", updated.Name)
		}
		if updated.Slug != "weather-api-development" {
			t.Errorf("store sandbox Slug not updated: %q", updated.Slug)
		}
	})

	t.Run("rename rejects blank name", func(t *testing.T) {
		body := map[string]string{"name": "   "}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/rename", bytes.NewReader(bodyBytes))
		req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, ownerUser)), "id", sb.ID)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for blank name, got %d", rec.Code)
		}
	})

	t.Run("viewer role cannot rename sandbox", func(t *testing.T) {
		body := map[string]string{"name": "Unauthorized Rename"}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest("POST", "/api/sandboxes/"+sb.ID+"/rename", bytes.NewReader(bodyBytes))
		req = withURLParam(req.WithContext(context.WithValue(req.Context(), ctxUserKey, viewerUser)), "id", sb.ID)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d", rec.Code)
		}
	})
}
