# API Sandbox Links — Complete Repository Overview & Module Inventory

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Principal Software Architect, Senior Go Engineer, Senior Next.js Engineer, DevOps Engineer, Application Security Engineer  

---

## 1. System Overview & Core Resource Model

API Sandbox Links is an ephemeral, isolated cloud development and testing sandbox environment platform. It enables engineers to import Git repositories, automatically analyze and containerize multi-language projects, edit source files in a browser-based Monaco workspace with hot-reloading, stream live runtime container logs, execute isolated terminal commands, manage encrypted secrets, collaborate with role-based team permissions, and preview live applications through a low-latency reverse proxy gateway.

### Authoritative Domain Hierarchy

```
User / GitHub Account
  └── Projects (Default Personal or Shareable Team Project)
        └── Repositories (Upstream Git URLs, branch tracking, sync state)
              └── Sandboxes (Isolated developer copies: config, slug, lifecycle state)
                    ├── Container Runtime (Disposable Docker application container)
                    ├── Preserved Working Directory (Filesystem workspace with copy-on-write)
                    ├── Encrypted Secret Store (AES-256-GCM protected environment variables)
                    └── Sidecars (Attached database engines: PostgreSQL, Redis, MySQL)
```

| Entity | System Responsibility | Lifespan & Persistence | Security Boundary |
|---|---|---|---|
| **User** | GitHub OAuth authenticated developer identity. Owns default project and GitHub access tokens. | Permanent (PostgreSQL `users` table). | Identity root, token encrypted at rest. |
| **Project** | Team collaboration boundary, member roles (`OWNER`, `EDITOR`, `VIEWER`), invitations. | Permanent until project owner explicitly deletes. | Cross-tenant isolation root. Members cannot exceed project role permissions. |
| **Repository** | Git metadata, upstream commit hashes, default branches, clone references. | Permanent or linked to projects. | Public or user-authorized private GitHub source. |
| **Sandbox** | Concrete developer working copy, custom subdomain, build configuration, lifecycle state. | Persistent workspace disk; disposable container runtime. | Per-subdomain Traefik routing, unprivileged container UID. |
| **Container** | Ephemeral process execution runtime created from Railpack, BuildKit, or Dockerfile. | Disposable; stops on idle hibernation, destroyed on expiration. | Rootless/isolated Docker network, CPU/RAM resource limits, no Docker socket access. |
| **Sidecar** | Dedicated container instance providing persistent backing services (e.g., PostgreSQL). | Paired with sandbox lifecycle. | Isolated internal Docker network; accessible only by owning sandbox. |

---

## 2. Directory & Component Inventory

```
api-sandbox-links/
├── backend/
│   ├── cmd/
│   │   ├── migrate/          # Standalone database migration runner
│   │   ├── rekey/            # AES-256 master key rotation utility
│   │   └── server/           # Main Go backend API server & gateway daemon
│   ├── internal/
│   │   ├── ai/               # Gemini 2.5 Flash client (env suggestions, OpenAPI generation)
│   │   ├── api/              # HTTP REST handlers, Chi router, middleware, auth, WebSockets
│   │   ├── cache/            # Redis distributed cache & sliding expiration store
│   │   ├── config/           # Centralized environment variable loader & configuration validation
│   │   ├── db/               # PostgreSQL connection, GORM models, atomic transactions, SQL queries
│   │   ├── envparse/         # Safe .env lexer, parser, quotation stripper, and normalizer
│   │   ├── git/              # Go-git operations, safe commit guard, secret scanning, untracking
│   │   ├── orchestrator/     # Analyzer, build engine, Railpack/Docker deployer, lifecycle state machine
│   │   ├── proxy/            # Go gateway, Traefik reverse proxy middleware, wake-on-request
│   │   ├── reaper/           # Background garbage collector for expired sandboxes & stale volumes
│   │   ├── runtime/          # Docker runner abstraction and container execution interfaces
│   │   ├── secret/           # AES-256-GCM envelope encryption & decryption engine
│   │   ├── security/         # File path confinement, symlink escape checks, secret pattern scanner
│   │   └── workspacelock/    # In-memory per-sandbox read/write lock for atomic Git & edit concurrency
│   └── migrations/           # Versioned SQL migrations (0001 through 0012)
├── frontend/
│   ├── app/
│   │   ├── (auth)/           # OAuth callback & login entry points
│   │   ├── (main)/
│   │   │   ├── dashboard/    # User sandbox listing, active status, quick actions
│   │   │   ├── projects/     # Team project management, member lists, invitations
│   │   │   ├── sandboxes/    # Main Sandbox Studio (Monaco, logs, collaborators, terminal, settings)
│   │   │   ├── profile/      # User profile, connected GitHub identity, activity history
│   │   │   └── upload/       # Import repository & launch sandbox wizard
│   │   └── api/              # Next.js BFF (Backend-For-Frontend) proxy routes with session forwarders
│   ├── components/           # Reusable UI components (Editor, FileTree, Settings, GitUI, Popups)
│   └── lib/                  # SWR client, fetchWithAuth, cookie helpers, formatting utilities
└── docs/                     # Architectural documentation, audit reports, and QA matrices
```

---

## 3. Detailed Subsystem Analysis

### 3.1 Backend API Server & Router (`backend/internal/api`)
- **Responsibility:** Serves HTTP REST endpoints, handles GitHub OAuth authentication, enforces role-based access control, coordinates sandbox mutations, and multiplexes WebSockets for live logs and Monaco file change synchronization.
- **Dependencies:** `github.com/go-chi/chi/v5`, `gorilla/websocket`, `gorm.io/gorm`, `backend/internal/orchestrator`, `backend/internal/git`.
- **Callers:** Frontend Next.js BFF proxy routes (`/api/*`), Traefik gateway reverse proxy.
- **Data Ownership:** HTTP session cookies, context user injection, REST request/response validation.
- **Failure Behavior:** Returns structured RFC 7807/JSON errors (`{"error": "message"}`), logs structured warn/error entries via `log/slog`, aborts mutations with database rollbacks.
- **Security Boundaries:** Validates session cookie or Bearer token on every request; enforces `requireProjectRole` and `requireSandboxRoleForMutation` before touching resources.

### 3.2 In-Browser Code Workspace & Git Engine (`backend/internal/git`, `frontend/components/editor`)
- **Responsibility:** Exposes read, write, create, delete, and rename operations for workspace files; runs pre-commit security validation against raw credentials; stages safe files and commits changes to Git; pushes to GitHub branches.
- **Dependencies:** `github.com/go-git/go-git/v5`, `backend/internal/security`, `backend/internal/workspacelock`.
- **Data Ownership:** Sandbox workspace filesystem on host disk (`/tmp/sandboxes/<subdomain>`).
- **Failure Behavior:** If a secret pattern or protected `.env` file is detected, aborts commit with HTTP 422 `CommitViolation` array without modifying git history or index.
- **Security Boundaries:** Enforces strict lexical confinement and symlink resolution via `security.OpenSandboxRoot(workDir)`. Prevents arbitrary path traversal, reading outside workspace, or following symlinks to `/etc/passwd`.

### 3.3 Container Orchestrator & Lifecycle Engine (`backend/internal/orchestrator`)
- **Responsibility:** Automatically detects languages (Node, Python, Go, Java, Rust, C/C++), selects build strategies (Railpack vs Dockerfile), dispatches image builds to BuildKit, creates Docker containers with network and memory constraints, and enforces authoritative lifecycle states (`QUEUED`, `BUILDING`, `RUNNING`, `HIBERNATED`, `EXPIRED`, `DELETED`).
- **Dependencies:** `github.com/docker/docker/client`, BuildKit daemon, PostgreSQL, Redis.
- **Data Ownership:** Docker container IDs, port allocations, image cache keys, lifecycle timestamps.
- **Failure Behavior:** Failed builds transition to `FAILED` with failure logs persisted in DB; failed runtimes trigger graceful container removal while preserving the user workspace on disk for 7 days.
- **Security Boundaries:** Disables container privileged mode; blocks Docker socket mounts; applies CPU (1.0 core) and memory (1GB) limits; attaches containers to an isolated Docker network.

### 3.4 Gateway & Wake-on-Request Proxy (`backend/internal/proxy`)
- **Responsibility:** Intercepts incoming HTTP requests to sandbox preview domains (`<subdomain>.127.0.0.1.nip.io`), inspects sandbox lifecycle state, automatically wakes `HIBERNATED` containers via singleflight deduplication, and proxies developer traffic directly to the active container.
- **Dependencies:** `net/http/httputil`, Go standard library, singleflight group.
- **Failure Behavior:** Returns an informative intermediate waking screen while starting; returns HTTP 410 Gone for expired/deleted sandboxes; handles backend timeouts gracefully.
- **Security Boundaries:** Resolves subdomain dynamically from request Host header; verifies valid format; rejects forged or invalid hosts with HTTP 404.

---

## 4. Module Inventory Summary Table

| Module | Core File(s) | Public Interfaces | Test Coverage | Status |
|---|---|---|---|---|
| **Config Loader** | `internal/config/config.go` | `Load()`, `Config` struct | Unit tests in `config_test.go` | **Production Ready** |
| **Database Models** | `internal/db/models.go`, `queries.go` | `GormStore`, `Store` interface | Transaction tests in `projects_test.go`, `cow_test.go` | **Production Ready** |
| **Secret Encryption** | `internal/secret/crypto.go` | `Encrypt()`, `Decrypt()` | Encryption tests in `secret_test.go` | **Production Ready** |
| **Secret Guard** | `internal/git/secret_guard.go` | `ValidatePreCommit()`, `SafeCommitAll()`, `UntrackFile()` | Unit tests in `secret_guard_test.go` | **Production Ready** |
| **Workspace Confinement** | `internal/security/path.go` | `SafeJoin()`, `OpenSandboxRoot()` | Security tests in `files_linux.go`, `security_test.go` | **Production Ready** |
| **Orchestrator Deployer** | `internal/orchestrator/deploy.go` | `DeploySandbox()`, `Teardown()` | Integration tests in `runtime_integration_test.go` | **Production Ready** |
| **Lifecycle Reaper** | `internal/reaper/reaper.go` | `Start()`, `Stop()`, `ReapOnce()` | Unit tests in `reaper_test.go` | **Production Ready** |
| **Next.js Client UI** | `frontend/app/(main)/sandboxes/[id]` | Full Sandbox Studio | Turbopack compilation & E2E specs | **Production Ready** |
