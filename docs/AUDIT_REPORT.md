# Repository Engineering Audit & Remediation Findings Report

**Repository:** `AyushCN/frontendformajor`  
**Branch:** `frontend`  
**Audit Date:** 2026-11-17  
**Scope:** Backend (Go), Frontend (Next.js), Persistence (PostgreSQL), Infrastructure, Lifecycle, Authentication, RBAC

---

## Executive Summary

A comprehensive engineering audit of the repository was conducted across the Go backend, Next.js frontend, database models and migrations, Traefik proxy, runtime orchestrator, authentication workflows, and collaboration features. 

The audit identified critical architectural gaps where project management, role-based access control, and invitation lifecycles were being executed in a transient JSON file store on the frontend instead of the authoritative PostgreSQL backend. Furthermore, multiple hardcoded developer identities, sample accounts, and insecure local storage authentication bypasses were identified.

The working Docker container sandbox runtime, builder detection engine, and sliding hibernation lifecycle are architecturally sound and will be preserved while resolving the persistence, authorization, and security layers.

---

## Detailed Findings Matrix

| Finding ID | Severity | Component | File Reference | Root Cause | Proposed Solution | Regression Risk |
|---|---|---|---|---|---|---|
| **F-01** | **Critical** | Persistence | [frontend/lib/projects-store.ts](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/lib/projects-store.ts) | Project and invitation data stored in ephemeral JSON files (`/tmp/api-sandbox-links/projects.json`). | Move projects, project repositories, memberships, and invitations to PostgreSQL with numbered SQL migration and GORM queries. | Low. Frontend endpoints will proxy to authoritative backend. |
| **F-02** | **Critical** | Security / RBAC | [backend/internal/api/sandboxes.go](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/sandboxes.go#L207-L228) | Sandbox access control only verifies direct repository owner, bypassing project collaboration roles (`Owner`, `Editor`, `Viewer`). | Implement project-scoped authorization in backend (`authorizeSandboxAccessWithRole`) enforcing Viewer (read-only), Editor (read/write/run), and Owner (manage/destroy). | Low. Strengthens security without impacting legitimate owners. |
| **F-03** | **High** | Identity / Auth | [frontend/lib/auth.tsx](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/lib/auth.tsx#L55-L62), [projects/route.ts](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/app/api/projects/route.ts) | Synthetic user identities (`AyushCN`, `user-ayushcn`, `owner@api-sandbox.local`) and `asl_dev_authed` local storage bypass. | Remove all hardcoded usernames/emails and client auth bypasses; bind session to authentic backend user profiles. | Medium. Requires authentic user session for all operations. |
| **F-04** | **High** | OAuth Security | [backend/internal/api/auth.go](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/auth.go#L56-L74) | GitHub OAuth `state` is timestamp-derived, not cryptographically secure, and not validated on callback (CSRF vulnerability). | Generate cryptographic random state, store in encrypted HTTP-only cookie, and validate before code exchange. | Low. Standard OAuth2 security practice. |
| **F-05** | **High** | Identity Contract | [backend/internal/api/auth.go](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/auth.go#L128-L140) | `/api/me` returns `github_id`, `username`, `avatar_url`, but omits primary `id` and `email`, forcing frontend fallbacks. | Update `/api/me` to return complete authoritative user identity `{ id, github_id, username, email, avatar_url }`. | Low. Backwards-compatible addition. |
| **F-06** | **High** | Credentials Leak | [backend/internal/api/auth.go](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/auth.go#L289) | `DevLogin` returns raw `User` struct including `GithubToken`. | Sanitize auth responses to omit internal tokens, hashes, and encryption keys. | Low. Prevents credential leakage. |
| **F-07** | **High** | Collaboration | [frontend/app/api/projects/[id]/invitations/route.ts](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/app/api/projects/[id]/invitations/route.ts) | Non-atomic invitation processing and fabrication of recipient IDs from username strings. | Atomic DB transactions for invite, accept, reject, revoke, and role changes bound strictly to registered user UUIDs. | Low. Eliminates race conditions and invalid recipient IDs. |
| **F-08** | **Medium** | Lifecycle | [backend/internal/proxy/wake_middleware.go](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/proxy/wake_middleware.go) | Wake gateway verifies access via `CanAccessSandbox`, but fallback logic requires table existence checks. | Unify `CanAccessSandbox` with PostgreSQL `project_members` and `project_sandboxes` relationships. | Low. Unifies authorization logic across API and Gateway. |

---

## Remediation Plan & Dependencies

1. **Phase 1: Database Schema & Migration**
   - Add `0006_projects_collaboration.up.sql` and `.down.sql`.
   - Tables: `projects`, `project_repositories`, `project_members`, `project_invitations`, `project_sandboxes`, `audit_events`.
   - Indexes and foreign keys with cascading delete where appropriate.

2. **Phase 2: Backend Store Implementation**
   - Define model structs in `backend/internal/db/models.go`.
   - Implement typed queries in `backend/internal/db/queries.go` with ACID transactions.
   - Update `backend/internal/api/store.go` interface.

3. **Phase 3: Backend Project & Collaboration Endpoints**
   - Implement `projects.go` handlers in Go backend.
   - Register routes under protected router in `backend/internal/api/router.go`.
   - Enforce RBAC (`OWNER`, `EDITOR`, `VIEWER`) across sandbox actions, file editing, and project settings.

4. **Phase 4: Authentication & OAuth Security Hardening**
   - Cryptographically random state parameter for OAuth consent URL.
   - Session-bound HTTP-only cookie for state verification.
   - Full identity contract in `/api/me`.
   - Sanitize DevLogin output and require explicit `dev_auth` config.

5. **Phase 5: Frontend Integration & Cleanup**
   - Remove `/tmp/api-sandbox-links/projects.json` file store.
   - Clean up fake user fallbacks in Next.js API routes, forwarding calls to the authoritative backend.
   - Remove `asl_dev_authed` local storage bypass.

6. **Phase 6: Testing & Quality Assurance**
   - Go unit tests, database queries tests, RBAC tests, race detector (`go test -race`).
   - Frontend type checks (`npx tsc --noEmit`) and production builds.
