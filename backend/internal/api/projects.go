package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/go-chi/chi/v5"
)

// projectJSON formats a db.Project for client consumption, supporting both
// modern and backwards-compatible field shapes.
func projectJSON(p *db.Project) map[string]any {
	if p == nil {
		return nil
	}

	collabs := make([]map[string]any, 0, len(p.Members))
	for _, m := range p.Members {
		collab := map[string]any{
			"id":        m.ID,
			"userId":    m.UserID,
			"role":      m.Role,
			"status":    m.Status,
			"invitedAt": m.InvitedAt.Format(time.RFC3339),
			"createdAt": m.CreatedAt.Format(time.RFC3339),
		}
		if m.InvitedBy != nil {
			collab["invitedBy"] = *m.InvitedBy
		}
		if m.AcceptedAt != nil {
			collab["acceptedAt"] = m.AcceptedAt.Format(time.RFC3339)
		}
		if m.User != nil {
			collab["user"] = map[string]any{
				"id":        m.User.ID,
				"username":  m.User.Username,
				"email":     m.User.Email,
				"avatarUrl": m.User.AvatarURL,
			}
		}
		collabs = append(collabs, collab)
	}

	repos := make([]map[string]any, 0, len(p.Repositories))
	for _, r := range p.Repositories {
		repos = append(repos, map[string]any{
			"id":            r.ID,
			"name":          r.Name,
			"gitUrl":        r.GitURL,
			"defaultBranch": r.DefaultBranch,
			"createdAt":     r.CreatedAt.Format(time.RFC3339),
		})
	}

	sandboxIds := make([]string, 0, len(p.Sandboxes))
	for _, s := range p.Sandboxes {
		sandboxIds = append(sandboxIds, s.SandboxID)
	}

	return map[string]any{
		"id":            p.ID,
		"name":          p.Name,
		"description":   p.Description,
		"isDefault":     p.IsDefault,
		"createdBy":     p.CreatedBy,
		"createdAt":     p.CreatedAt.Format(time.RFC3339),
		"updatedAt":     p.UpdatedAt.Format(time.RFC3339),
		"members":       collabs,
		"collaborators": collabs,
		"repositories":  repos,
		"sandboxIds":    sandboxIds,
	}
}

// handleListProjects returns all projects accessible to the authenticated user.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	projects, err := s.store.ListUserProjects(user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list projects: "+err.Error())
		return
	}

	out := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateProject creates a new user-owned project.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "project name is required")
		return
	}

	project := &db.Project{
		Name:        name,
		Description: strings.TrimSpace(req.Description),
	}

	created, err := s.store.CreateProject(project, user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, projectJSON(created))
}

// handleGetProject returns a single project by ID.
func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	// If requesting "default-project", map to user's personal default space
	var project *db.Project
	var err error
	if id == "default-project" {
		project, err = s.store.GetOrCreateDefaultProject(user.ID)
	} else {
		project, err = s.store.GetProject(id)
	}

	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	// Verify authorization
	role, err := s.store.GetProjectRole(project.ID, user.ID)
	if err != nil || role == "" {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	writeJSON(w, http.StatusOK, projectJSON(project))
}

// handleUpdateProject updates project name and description.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := s.store.UpdateProject(id, user.ID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description))
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, projectJSON(updated))
}

// handleDeleteProject deletes a project.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	if err := s.store.DeleteProject(id, user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

// handleTransferProjectOwnership transfers project ownership to another user.
func (s *Server) handleTransferProjectOwnership(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	var req struct {
		NewOwnerID string `json:"newOwnerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.NewOwnerID) == "" {
		writeErr(w, http.StatusBadRequest, "newOwnerId required")
		return
	}

	if err := s.store.TransferProjectOwnership(id, strings.TrimSpace(req.NewOwnerID), user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "newOwnerId": req.NewOwnerID})
}

// handleListProjectMembers lists active members and pending invitations for the UI.
func (s *Server) handleListProjectMembers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	var project *db.Project
	var err error
	if id == "default-project" {
		project, err = s.store.GetOrCreateDefaultProject(user.ID)
	} else {
		project, err = s.store.GetProject(id)
	}

	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	role, err := s.store.GetProjectRole(project.ID, user.ID)
	if err != nil || role == "" {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	members, err := s.store.ListProjectMembers(project.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list members: "+err.Error())
		return
	}

	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		item := map[string]any{
			"id":        m.ID,
			"userId":    m.UserID,
			"role":      m.Role,
			"status":    m.Status,
			"invitedAt": m.InvitedAt.Format(time.RFC3339),
			"createdAt": m.CreatedAt.Format(time.RFC3339),
		}
		if m.AcceptedAt != nil {
			item["acceptedAt"] = m.AcceptedAt.Format(time.RFC3339)
		}
		if m.User != nil {
			item["user"] = map[string]any{
				"id":        m.User.ID,
				"username":  m.User.Username,
				"email":     m.User.Email,
				"avatarUrl": m.User.AvatarURL,
			}
		}
		out = append(out, item)
	}

	// Also merge pending invitations for project owners
	if role == db.ProjectMemberRoleOwner && !project.IsDefault {
		if invs, err := s.store.ListPendingProjectInvitations(project.ID); err == nil {
			for _, inv := range invs {
				item := map[string]any{
					"id":           inv.ID,
					"userId":       inv.InviteeID,
					"role":         inv.Role,
					"status":       "PENDING",
					"isInvitation": true,
					"inviterId":    inv.InviterID,
					"invitedAt":    inv.CreatedAt.Format(time.RFC3339),
					"expiresAt":    inv.ExpiresAt.Format(time.RFC3339),
				}
				if inv.Invitee != nil {
					item["user"] = map[string]any{
						"id":        inv.Invitee.ID,
						"username":  inv.Invitee.Username,
						"email":     inv.Invitee.Email,
						"avatarUrl": inv.Invitee.AvatarURL,
					}
				}
				out = append(out, item)
			}
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// handleUpdateMemberRole updates a member's role.
func (s *Server) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userId")

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	newRole := db.ProjectMemberRole(strings.ToUpper(strings.TrimSpace(req.Role)))
	if newRole != db.ProjectMemberRoleEditor && newRole != db.ProjectMemberRoleViewer {
		writeErr(w, http.StatusBadRequest, "invalid role: must be EDITOR or VIEWER")
		return
	}

	if err := s.store.UpdateMemberRole(id, targetUserID, newRole, user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "role": newRole})
}

// handleRemoveProjectMember removes a member.
func (s *Server) handleRemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userId")

	if err := s.store.RemoveProjectMember(id, targetUserID, user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": targetUserID})
}

// handleCreateProjectInvitation sends an invitation.
func (s *Server) handleCreateProjectInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	var req struct {
		InviteeID string `json:"inviteeId"`
		Role      string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	role := db.ProjectMemberRole(strings.ToUpper(strings.TrimSpace(req.Role)))
	if role != db.ProjectMemberRoleEditor && role != db.ProjectMemberRoleViewer {
		writeErr(w, http.StatusBadRequest, "invalid role: must be EDITOR or VIEWER")
		return
	}

	inv := &db.ProjectInvitation{
		ProjectID: id,
		InviterID: user.ID,
		InviteeID: strings.TrimSpace(req.InviteeID),
		Role:      role,
	}

	if err := s.store.CreateInvitation(inv); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	created, _ := s.store.GetInvitation(inv.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":         true,
		"invitation": created,
	})
}

// handleListProjectInvitations lists pending invitations for a project.
func (s *Server) handleListProjectInvitations(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || role != db.ProjectMemberRoleOwner {
		writeErr(w, http.StatusForbidden, "only project owners can view project invitations")
		return
	}

	invs, err := s.store.ListPendingProjectInvitations(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invs)
}

// handleListUserInvitations lists pending invitations addressed to the user.
func (s *Server) handleListUserInvitations(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	invs, err := s.store.ListUserInvitations(user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invs)
}

// handleAcceptInvitation accepts an invitation.
func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	if err := s.store.AcceptInvitation(id, user.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": id})
}

// handleRejectInvitation rejects an invitation.
func (s *Server) handleRejectInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	if err := s.store.RejectInvitation(id, user.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rejected": id})
}

// handleRevokeInvitation revokes an invitation.
func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	if err := s.store.RevokeInvitation(id, user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": id})
}

// handleResendInvitation extends an invitation.
func (s *Server) handleResendInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	if err := s.store.ResendInvitation(id, user.ID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resent": id})
}

// handleListProjectRepositories lists repositories connected to a project.
func (s *Server) handleListProjectRepositories(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || role == "" {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	repos, err := s.store.ListProjectRepositories(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// handleAddProjectRepository adds a repository to a project.
func (s *Server) handleAddProjectRepository(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || (role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor) {
		writeErr(w, http.StatusForbidden, "only project owners and editors can add repositories")
		return
	}

	var req struct {
		Name          string `json:"name"`
		GitURL        string `json:"gitUrl"`
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.GitURL) == "" {
		writeErr(w, http.StatusBadRequest, "gitUrl is required")
		return
	}

	branch := strings.TrimSpace(req.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		parts := strings.Split(strings.TrimSuffix(req.GitURL, ".git"), "/")
		name = parts[len(parts)-1]
	}

	repo := &db.ProjectRepository{
		ProjectID:     id,
		Name:          name,
		GitURL:        strings.TrimSpace(req.GitURL),
		DefaultBranch: branch,
	}

	if err := s.store.AddProjectRepository(repo); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, repo)
}

// handleRemoveProjectRepository removes a repository from a project.
func (s *Server) handleRemoveProjectRepository(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")
	repoID := chi.URLParam(r, "repoId")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || (role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor) {
		writeErr(w, http.StatusForbidden, "only project owners and editors can remove repositories")
		return
	}

	if err := s.store.RemoveProjectRepository(id, repoID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": repoID})
}

// handleListProjectSandboxes lists sandboxes associated with a project.
func (s *Server) handleListProjectSandboxes(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || role == "" {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	sandboxes, err := s.store.ListProjectSandboxes(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]map[string]any, 0, len(sandboxes))
	for _, sb := range sandboxes {
		out = append(out, s.sandboxJSON(sb))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAssociateProjectSandbox links a sandbox to a project.
func (s *Server) handleAssociateProjectSandbox(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || (role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor) {
		writeErr(w, http.StatusForbidden, "only project owners and editors can link sandboxes")
		return
	}

	var req struct {
		SandboxID string `json:"sandboxId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SandboxID) == "" {
		writeErr(w, http.StatusBadRequest, "sandboxId required")
		return
	}

	sbID := strings.TrimSpace(req.SandboxID)
	owns, err := s.store.OwnsSandbox(sbID, user.ID)
	if err != nil || !owns {
		writeErr(w, http.StatusForbidden, "you do not own this sandbox")
		return
	}

	if err := s.store.AssociateProjectSandbox(id, sbID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "linked": sbID})
}

// handleDisassociateProjectSandbox unlinks a sandbox from a project.
func (s *Server) handleDisassociateProjectSandbox(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")
	sbID := chi.URLParam(r, "sandboxId")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || (role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor) {
		writeErr(w, http.StatusForbidden, "only project owners and editors can unlink sandboxes")
		return
	}

	if err := s.store.DisassociateProjectSandbox(id, sbID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unlinked": sbID})
}

// handleListProjectActivity returns audit events for a project.
func (s *Server) handleListProjectActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id := chi.URLParam(r, "id")

	role, err := s.store.GetProjectRole(id, user.ID)
	if err != nil || role == "" {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	events, err := s.store.ListAuditEvents(id, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// authorizeSandboxAccessWithRole checks whether user has at least minRole for the sandbox.
// VIEWER: can view files, logs, status, preview.
// EDITOR: can view + write files, commit, restart, deploy.
// OWNER: full control, settings, delete.
func (s *Server) authorizeSandboxAccessWithRole(r *http.Request, minRole db.ProjectMemberRole) (*db.Sandbox, error) {
	user, ok := s.userFrom(r)
	if !ok {
		return nil, errors.New("not signed in")
	}

	id := chi.URLParam(r, "id")
	// 1. Direct ownership grants full OWNER privileges
	if owns, err := s.store.OwnsSandbox(id, user.ID); err == nil && owns {
		sb, err := s.store.GetSandbox(id)
		if err != nil {
			return nil, errSandboxForbidden
		}
		return sb, nil
	}

	// 2. Otherwise check project collaboration role
	role, err := s.store.GetSandboxRole(id, user.ID)
	if err != nil {
		return nil, errSandboxForbidden
	}

	// Role hierarchy: OWNER > EDITOR > VIEWER
	switch minRole {
	case db.ProjectMemberRoleViewer:
		// Any active role suffices
	case db.ProjectMemberRoleEditor:
		if role != db.ProjectMemberRoleOwner && role != db.ProjectMemberRoleEditor {
			return nil, errors.New("forbidden: viewers have read-only access and cannot modify files or execute actions")
		}
	case db.ProjectMemberRoleOwner:
		if role != db.ProjectMemberRoleOwner {
			return nil, errors.New("forbidden: action requires project owner privileges")
		}
	}

	sb, err := s.store.GetSandbox(id)
	if err != nil {
		return nil, errSandboxForbidden
	}
	return sb, nil
}
