package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/envparse"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
	"github.com/api-sandbox-links/backend/internal/security"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// EnvVarResponse is the serialization envelope for environment variables.
type EnvVarResponse struct {
	Key           string    `json:"key"`
	Value         string    `json:"value"`
	Masked        bool      `json:"masked"`
	IsAISuggested bool      `json:"is_ai_suggested"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// handleListEnvVars returns the environment variables for a sandbox.
// Secrets are masked by default. Reveal is only allowed for authorized Owners/Secrets Managers.
func (s *Server) handleListEnvVars(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}

	canManage, _ := s.store.CanManageSecretsForSandbox(sb.ID, user.ID)
	reveal := r.URL.Query().Get("reveal") == "true" && canManage

	vars, err := s.store.ListEnvVars(sb.ID)
	if err != nil {
		s.logger.Warn("listing env vars failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "could not list environment variables")
		return
	}

	out := make([]EnvVarResponse, 0, len(vars))
	for _, v := range vars {
		val := "••••••••"
		masked := true

		if reveal {
			plain, dErr := s.box.Decrypt(v.Value)
			if dErr == nil {
				val = string(plain)
				masked = false
			}
		}

		out = append(out, EnvVarResponse{
			Key:           v.Key,
			Value:         val,
			Masked:        masked,
			IsAISuggested: v.IsAISuggested,
			CreatedAt:     v.CreatedAt,
			UpdatedAt:     v.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox_id": sb.ID,
		"env_vars":   out,
		"can_manage": canManage,
	})
}

// setEnvVarRequest is the JSON payload for creating/updating a single variable.
type setEnvVarRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// handleSetEnvVar sets or updates a single environment variable.
func (s *Server) handleSetEnvVar(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}

	canManage, err := s.store.CanManageSecretsForSandbox(sb.ID, user.ID)
	if err != nil || !canManage {
		writeErr(w, http.StatusForbidden, "you do not have permission to manage secrets for this sandbox")
		return
	}

	var req setEnvVarRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "key is required")
		return
	}
	if len(key) > envparse.MaxKeyLength {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("key exceeds maximum length of %d", envparse.MaxKeyLength))
		return
	}
	if len(req.Value) > envparse.MaxValueLength {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("value exceeds maximum length of %d", envparse.MaxValueLength))
		return
	}

	// Validate key identifier
	if _, err := envparse.Parse(key + "=placeholder"); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid variable name %q", key))
		return
	}

	enc, err := s.box.Encrypt([]byte(req.Value))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	if err := s.store.UpsertEnvVar(sb.ID, key, enc, false); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save environment variable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true,
		"key":   key,
	})
}

// handleDeleteEnvVar removes a single environment variable.
func (s *Server) handleDeleteEnvVar(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}

	canManage, err := s.store.CanManageSecretsForSandbox(sb.ID, user.ID)
	if err != nil || !canManage {
		writeErr(w, http.StatusForbidden, "you do not have permission to manage secrets for this sandbox")
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeErr(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := s.store.DeleteEnvVar(sb.ID, key); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete environment variable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"key":     key,
	})
}

// uploadEnvRequest represents raw .env text or merged payload.
type uploadEnvRequest struct {
	Content  string `json:"content"`
	Strategy string `json:"strategy"` // "merge" or "replace"
}

// handleUploadEnv parses and stores variables from an uploaded or pasted .env file.
func (s *Server) handleUploadEnv(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}

	canManage, err := s.store.CanManageSecretsForSandbox(sb.ID, user.ID)
	if err != nil || !canManage {
		writeErr(w, http.StatusForbidden, "you do not have permission to manage secrets for this sandbox")
		return
	}

	var content string
	var strategy string

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// Read multipart file
		if err := r.ParseMultipartForm(int64(envparse.MaxFileSize) + 1024); err != nil {
			writeErr(w, http.StatusBadRequest, "failed to parse multipart form")
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "file field is required")
			return
		}
		defer file.Close()
		b, err := io.ReadAll(io.LimitReader(file, int64(envparse.MaxFileSize)+1))
		if err != nil || len(b) > envparse.MaxFileSize {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("file exceeds maximum size limit of %d bytes", envparse.MaxFileSize))
			return
		}
		content = string(b)
		strategy = r.FormValue("strategy")
	} else {
		var req uploadEnvRequest
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		content = req.Content
		strategy = req.Strategy
	}

	if strings.TrimSpace(content) == "" {
		writeErr(w, http.StatusBadRequest, "content is empty")
		return
	}

	entries, err := envparse.Parse(content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	existingVars, err := s.store.ListEnvVars(sb.ID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusInternalServerError, "failed to check existing variables")
		return
	}
	existingMap := make(map[string]bool)
	for _, v := range existingVars {
		existingMap[v.Key] = true
	}

	encMap := make(map[string][]byte, len(entries))
	appliedCount := 0
	skippedCount := 0

	for _, entry := range entries {
		if strategy == "merge" && existingMap[entry.Key] {
			skippedCount++
			continue
		}
		enc, err := s.box.Encrypt([]byte(entry.Value))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encMap[entry.Key] = enc
		appliedCount++
	}

	if strategy == "replace" {
		// Remove all existing variables first
		_ = s.store.DeleteAllEnvVars(sb.ID)
	}

	if err := s.store.BulkUpsertEnvVars(sb.ID, encMap, false); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save environment variables")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"uploaded": true,
		"applied":  appliedCount,
		"skipped":  skippedCount,
		"total":    len(entries),
	})
}

// handleDetectEnvRequirements inspects .env.example, .env.sample, and repository manifests
// to discover declared environment variable requirements.
func (s *Server) handleDetectEnvRequirements(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.requireSandboxRole(w, r, db.ProjectMemberRoleViewer)
	if !ok {
		return
	}

	workDir := filepath.Join(s.cfg.SandboxWorkDir, sb.Subdomain)
	requiredKeys := make(map[string]string) // key -> description/example

	// 1. Inspect .env.example / .env.sample / .env.template / .env.defaults
	candidates := []string{
		".env.example", ".env.sample", ".env.template", ".env.dist",
		".env.defaults", ".env.local.example", "env.example",
	}
	for _, candidate := range candidates {
		data, err := readWorkspaceFile(workDir, candidate)
		if err == nil && len(data) > 0 {
			if entries, err := envparse.Parse(string(data)); err == nil {
				for _, e := range entries {
					if _, exists := requiredKeys[e.Key]; !exists {
						requiredKeys[e.Key] = e.Value
					}
				}
			}
		}
	}

	// 1b. If no template files found, check entry files for process.env or os.Getenv references
	if len(requiredKeys) == 0 {
		codeCandidates := []string{"server.js", "index.js", "app.js", "main.py", "app.py", "main.go"}
		envPattern := regexp.MustCompile(`(?:process\.env\.|os\.Getenv\("?|os\.environ(?:\["|\.get\("))([A-Z][A-Z0-9_]{2,})`)
		for _, fname := range codeCandidates {
			data, err := readWorkspaceFile(workDir, fname)
			if err == nil && len(data) > 0 {
				matches := envPattern.FindAllStringSubmatch(string(data), -1)
				for _, m := range matches {
					if len(m) > 1 {
						k := m[1]
						if k != "NODE_ENV" && k != "PATH" && k != "PORT" && k != "HOME" {
							if _, exists := requiredKeys[k]; !exists {
								requiredKeys[k] = "Detected in " + fname
							}
						}
					}
				}
			}
		}
	}

	// 2. Fetch configured keys in DB
	configuredVars, _ := s.store.ListEnvVars(sb.ID)
	configuredMap := make(map[string]bool)
	for _, v := range configuredVars {
		configuredMap[v.Key] = true
	}

	type RequiredVar struct {
		Key          string `json:"key"`
		ExampleValue string `json:"example_value,omitempty"`
		Configured   bool   `json:"configured"`
	}

	var detected []RequiredVar
	missingCount := 0

	for k, example := range requiredKeys {
		isConfigured := configuredMap[k]
		if !isConfigured {
			missingCount++
		}
		detected = append(detected, RequiredVar{
			Key:          k,
			ExampleValue: example,
			Configured:   isConfigured,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox_id": sb.ID,
		"required":   detected,
		"missing":    missingCount,
		"has_saved":  len(configuredVars) > 0,
	})
}

// handleApplyEnvToRuntime restarts the running sandbox container to inject the latest environment variables.
func (s *Server) handleApplyEnvToRuntime(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	sb, ok := s.requireSandboxRoleForMutation(w, r)
	if !ok {
		return
	}

	canManage, err := s.store.CanManageSecretsForSandbox(sb.ID, user.ID)
	if err != nil || !canManage {
		writeErr(w, http.StatusForbidden, "you do not have permission to apply environment changes")
		return
	}

	if sb.Status != db.StatusRunning || sb.ContainerID == "" {
		writeErr(w, http.StatusConflict, "sandbox is not currently running")
		return
	}

	// Deploy synchronously/background with TriggerManual or restart
	started := s.goBackground(func(ctx context.Context) {
		deployCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if err := s.deployer.DeploySandbox(deployCtx, sb, orchestrator.DeployOptions{
			Trigger: db.TriggerManual,
		}); err != nil {
			s.logger.Warn("redeploy after env update failed", slog.String("sandbox", sb.ID), slog.String("err", err.Error()))
		}
	})

	if !started {
		writeErr(w, http.StatusServiceUnavailable, "server is shutting down")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "updating",
		"message": "restarting sandbox container with latest environment variables",
	})
}

// buildProtectedEnvView constructs a safe masked representation of .env for Monaco Editor.
func (s *Server) buildProtectedEnvView(ctx context.Context, sandboxID, rawContent string) string {
	entries, err := envparse.Parse(rawContent)
	if err != nil {
		return "# Protected environment file\n# Secrets are stored securely in database.\n# Visit Sandbox Settings > Environment Variables to manage.\n"
	}

	var sb strings.Builder
	sb.WriteString("# -------------------------------------------------------------\n")
	sb.WriteString("# PROTECTED ENVIRONMENT CONFIGURATION\n")
	sb.WriteString("# Actual secret values are encrypted at rest and masked below.\n")
	sb.WriteString("# Manage these values via the Environment Variables dashboard.\n")
	sb.WriteString("# -------------------------------------------------------------\n\n")

	for _, e := range entries {
		masked := security.MaskSecretValue(e.Value)
		sb.WriteString(fmt.Sprintf("%s=%s\n", e.Key, masked))
	}

	return sb.String()
}

// readWorkspaceFile safely reads a file within workDir using os.OpenRoot.
func readWorkspaceFile(workDir, relPath string) ([]byte, error) {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(relPath)
}
