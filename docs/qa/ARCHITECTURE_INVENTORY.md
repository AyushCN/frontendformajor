# API Sandbox Links — Architecture & System Inventory

> **Document Version:** 1.0.0  
> **Target System:** API Sandbox Links (Go Backend, Next.js 16 Frontend, PostgreSQL, Redis, Docker/Railpack, Traefik, WebSockets)  
> **Audit Date:** November 2026  
> **Auditor:** Senior QA Architect & Systems Engineering Team  

---

## 1. System Topology & Architectural Overview

API Sandbox Links is a multi-tenant cloud development platform and ephemeral sandbox execution environment. It enables developers to import Git repositories, dynamically detect build runtimes (Node.js, Python, Go, Java, Dockerfile, etc.), compile container images via Railpack and BuildKit, mount persistent workspaces with Monaco Editor live synchronization, inject encrypted environment secrets, and expose live preview URLs with automatic idle hibernation and wake-on-request.

```
                                  [ User Browser / Client ]
                                             │
                       ┌─────────────────────┴─────────────────────┐
                       │                                           │
             (HTTPS / Dashboard)                        (HTTP Subdomain / Wildcard)
                       │                                           │
                       ▼                                           ▼
            [ Next.js 16 Frontend ]                         [ Traefik Proxy ]
              (Turbopack / BFF)                                    │
                       │                                           ▼
                       │ (Proxy /api/*)              [ Go Gateway (Wake-on-Request) ]
                       ▼                                           │
              [ Go API Server ]                                    │ (Forward Traffic)
           (Chi Router / Middleware)                               │
                       │                                           │
         ┌─────────────┼──────────────┬─────────────┐              │
         ▼             ▼              ▼             ▼              ▼
   [ PostgreSQL ]   [ Redis ]    [ Docker API ] [ SecretBox ] [ Sandbox Container ]
   (Auth/Projects   (Sessions/   (Railpack/     (AES-256-GCM   (CapDrop: ALL,
    Sandboxes/RBAC)  Ops/Locks)   BuildKit/Dev)  At-Rest)       512MB RAM, PIDs)
```

---

## 2. Component Inventory & Flow Mapping

### 2.1 Component Mapping Matrix

| Subsystem | Frontend Component | Next.js API Proxy | Backend Handler / Service | DB Model / Tables | External Dependencies | Implementation Status |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Authentication** | `lib/auth.tsx`, `app/(auth)/login` | N/A (Direct) | `handleAuthStart`, `handleAuthCallback`, `handleDevLogin`, `handleLogout` | `users` | GitHub OAuth, Redis (Sessions) | **Implemented** |
| **User Profile** | `app/(main)/profile/page.tsx` | N/A | `handleMe`, `handleSearchUsers` | `users`, `audit_events` | PostgreSQL | **Implemented** |
| **Projects & RBAC** | `app/(main)/projects/page.tsx`, `projects/[id]/page.tsx` | `app/api/projects/*` | `handleListProjects`, `handleCreateProject`, `handleUpdateProject`, `handleDeleteProject` | `projects`, `project_members`, `project_invitations` | PostgreSQL | **Implemented** |
| **Project Repositories** | `projects/[id]/page.tsx` (Repo Tab) | `app/api/projects/[id]/repositories` | `handleListProjectRepositories`, `handleAddProjectRepository`, `handleRemoveProjectRepository` | `repositories`, `project_repositories` | GitHub API | **Implemented** |
| **Project Collaborators**| `components/editor/InviteCollaboratorModal.tsx` | `app/api/projects/[id]/invite` | `handleCreateProjectInvitation`, `handleAcceptInvitation`, `handleUpdateMemberRole` | `project_members`, `project_invitations` | Mailer / Notification (Stub) | **Implemented** |
| **Sandbox Creation** | `app/(main)/upload/page.tsx` | `app/api/sandboxes`, `app/api/environments` | `handleCreateSandbox`, `CreateSandboxWithProject` | `sandboxes`, `project_sandboxes`, `deployments` | Docker, Railpack, Redis Quota Locks | **Implemented** |
| **Sandbox Lifecycle**| `components/EnvironmentSettings.tsx` | `app/api/environments/[id]/lifecycle/status` | `handleLifecycleStatus`, `handleExtendLifetime`, `handleDestroySandbox` | `sandboxes`, `lifetime_warnings` | PostgreSQL, Docker Engine | **Implemented** |
| **Build & Deploy** | `app/(main)/sandboxes/[id]/page.tsx` (Logs Tab) | `app/api/sandboxes/[id]/deployments` | `handleRedeploy`, `Deployer.Deploy` | `deployments` | BuildKit, Docker API, Railpack | **Implemented** |
| **Live Editor (Monaco)**| `components/editor/*`, `FileTree.tsx` | `app/api/environments/[id]/file*` | `handleFileTree`, `handleFileContent`, `handleSaveFile` | `file_edits` | Host Workspace Filesystem | **Implemented** |
| **Git Operations** | `components/GitUI.tsx`, `CommitModal` | `app/api/environments/[id]/git/*` | `handleCommitPush`, `handleUntrackFile`, `handleUpdateBranch` | Git index on disk | `go-git`, `internal/git/secret_guard` | **Implemented** |
| **Environment Secrets**| `components/EnvironmentVariablesManager.tsx` | `app/api/environments/[id]/env/*` | `handleListEnvVars`, `handleSetEnvVar`, `handleUploadEnv`, `handleApplyEnvToRuntime` | `env_vars` | AES-256-GCM (`secret.Box`) | **Implemented** |
| **Sandbox Transfer** | `components/editor/TransferSandboxModal.tsx` | `app/api/sandboxes/[id]/transfer` | `handleTransferSandbox`, `TransferSandbox` | `project_sandboxes`, `audit_events` | PostgreSQL (Atomic Locks) | **Implemented** |
| **Sandbox Naming** | Header Rename Modal, `EnvironmentSettings` | `app/api/sandboxes/[id]/rename` | `handleRenameSandbox`, `NormalizeSlug` | `sandboxes`, `sandbox_subdomain_aliases` | PostgreSQL, Traefik Router | **Implemented** |
| **Traffic Gateway** | Browser Preview Tab (`*.sandbox.localhost`) | N/A (Direct Gateway Listener :8090) | `proxy.Gateway.Handler`, `wake_middleware` | `sandboxes` | Traefik, Docker API (Wake) | **Implemented** |
| **WebSocket Logs** | Terminal / XTerm.js | `sandboxes/[id]/logs/ws` (Rewritten) | `handleLogsWS`, `handleStatsWS` | N/A | Gorilla WebSocket, Docker Demuxer | **Implemented** |
| **Idle Reaper** | N/A (Background Worker) | N/A | `IdleReaper.Run`, `reaper/idle_reaper.go` | `sandboxes` | Redis (Active Ops), Docker API | **Implemented** |
| **Expiry Reaper** | N/A (Background Worker) | N/A | `ExpiryReaper.Run`, `reaper/expiry_reaper.go`| `sandboxes` (Tombstones) | Docker API, Volume Storage | **Implemented** |
| **Database Sidecars** | Config dropdowns | N/A | `db_sidecars.go` (Embedded in deployer) | `sandbox_databases` | Docker Engine (Postgres/Redis) | **Partially Implemented** |

---

## 3. Detailed Request & Data Flow Tracing

### 3.1 Sandbox Creation & Project Association Flow
1. **Client Submission:** User selects/enters a repository URL, branch, display name, and destination project in `/upload`. Next.js BFF forwards `POST /api/sandboxes` to Go API.
2. **Authorization & Payload Validation:** Go handler `handleCreateSandbox` asserts valid session (`user_id`). Verifies project membership (`CanAccessProject` with Owner or Editor permissions). If no project is provided, falls back authoritatively to `GetOrCreateDefaultProject(user.ID)`.
3. **Atomic Quota & Database Lock:** An advisory lock is acquired in PostgreSQL. Active sandbox limits (`max_active_sandboxes`) and running limits (`max_running_sandboxes`) are validated.
4. **Normalized Identifiers:** Generates canonical display name (`Weather API`), URL-safe slug (`weather-api`), unique identifier (6 hex chars, e.g. `1db5cc`), and subdomain (`weather-api-1db5cc`).
5. **Atomic DB Insertion:** In a single transaction:
   - Inserts `sandboxes` row (status: `QUEUED`, `dev_mode: true`).
   - Inserts `project_sandboxes` row enforcing `UNIQUE (sandbox_id)` constraint.
   - Decoupled: `project_repositories` is linked for navigation without mutating global repo ownership.
6. **Async Orchestration Launch:** `goBackground` spawns `deployer.Deploy()` with context tracking. Workspace filesystem is initialized at `/var/lib/sandboxes/<id>/workspace`.
7. **Build & Container Launch:** Railpack/BuildKit runs. Container is initialized with bind mount to the workspace, capability drop policy applied, and Traefik router rules configured. Status transitions to `RUNNING`.

### 3.2 In-Browser Monaco Save & Hot Reload Synchronization
1. **User Edit:** User modifies a file in Monaco editor and triggers Auto-Save or Ctrl+S.
2. **API Request:** Frontend calls `PUT /api/environments/[id]/file` with file path and contents.
3. **RBAC & Path Traversal Check:**
   - Handler asserts user is not `VIEWER` (viewers are strictly read-only, HTTP 403).
   - Sanitizes path via `filepath.Clean`. Rejects any path escaping sandbox workspace (`..`, leading `/`, null bytes) with HTTP 400.
4. **Workspace Lock & Atomic Write:**
   - Acquires mutex lock on sandbox workspace via `workspacelock.Manager`.
   - Writes directly to host filesystem `/var/lib/sandboxes/<id>/workspace/<relPath>`.
5. **Direct Container Reflection:**
   - Because the development container mounts the workspace via host bind-mount (`/var/lib/sandboxes/<id>/workspace:/workspace`), the filesystem update is immediately visible inside the container without requiring image re-compilation.
   - Vite / Next.js / Nodemon watcher inside the container detects inotify event and triggers hot reload.

### 3.3 Gateway Wake-on-Request Flow
1. **Traffic Ingress:** Traefik catches `*.sandbox.localhost` or `*.127.0.0.1.nip.io` and routes HTTP request to Go Gateway (`:8090`).
2. **Subdomain Lookup:** Gateway extracts subdomain header/host, queries PostgreSQL for matching sandbox.
3. **State Evaluation:**
   - If `RUNNING`: Updates `last_accessed_at` in Redis/DB, proxies traffic directly to container IP:Port.
   - If `HIBERNATED`: Intercepts connection.
4. **Single-Flight Wake Execution:**
   - Locks `wakingMu` with sandbox ID key so 50 concurrent requests share one wake operation.
   - Executes `docker start <container_id>`.
   - Executes TCP/HTTP readiness probe until application responds or timeout (90s).
   - Transitions DB status to `RUNNING`, resets sliding expiration countdown.
   - Proxies the suspended HTTP request to the running application.

### 3.4 Sandbox Transfer Flow
1. **Transfer Modal:** Owner opens "Transfer Sandbox", chooses destination project.
2. **Atomic DB Transaction:**
   - Calls `TransferSandbox(sandboxID, destProjectID, actorID)`.
   - Locks `sandboxes` and `project_sandboxes` rows using `SELECT FOR UPDATE`.
   - Asserts actor has `OWNER` or `EDITOR` permissions in destination project.
   - Asserts actor owns the sandbox or owns the source project.
   - Checks that sandbox is not `EXPIRED` or `DELETED`.
   - Updates `project_sandboxes.project_id = destProjectID`.
   - Does **not** transfer or overwrite source repository links.
   - Records structured `TRANSFER_SANDBOX` audit record.
3. **Immediate Scoping:** Listing sandboxes for destination project immediately displays transferred sandbox; source project members without access to destination immediately lose access.

---

## 4. Discovered Inconsistencies & Defect Register

During this initial architectural trace, the following critical structural concerns were recorded:

| Risk ID | Subsystem | Severity | Description | Source File Reference |
| :--- | :--- | :--- | :--- | :--- |
| **RISK-001** | Git Pre-commit | **P1 (High)** | In `handleCommitPush`, untracked sensitive files (like `.env`) created locally inside the workspace might be auto-added if a broad `git add -A` is executed without prior `ValidatePreCommit` checks. | `backend/internal/api/commit_push.go:62` |
| **RISK-002** | WebSocket Auth | **P1 (High)** | `handleLogsWS` performs authentication and access validation during initial handshake, but does not actively disconnect when a user's project membership is revoked mid-stream. | `backend/internal/api/websocket.go:45` |
| **RISK-003** | Gateway Timeout | **P2 (Medium)** | Wake timeout is hardcoded to 90 seconds. Heavy applications (e.g. Java Spring Boot, large Python ML) may exceed this on cold startup, returning a 504 Gateway Timeout while the container continues booting. | `backend/internal/proxy/wake_middleware.go:30` |
| **RISK-004** | DB Migrations | **P2 (Medium)** | If migration 0012 runs on a legacy database with pre-existing duplicate `sandbox_id` associations, the deduplication query must preserve the newest active record safely before applying the unique index. | `backend/migrations/0012_*.sql` |
| **RISK-005** | Database Sidecars | **P3 (Low)** | `sandbox_databases` table exists with engine definitions, but sidecar provisioning logic is partially coupled to legacy Docker Compose paths rather than isolated standalone containers. | `backend/internal/orchestrator/` |
