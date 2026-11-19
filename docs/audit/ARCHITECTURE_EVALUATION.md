# Architecture Evaluation & Subsystem Analysis

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Principal Software Architect, Senior Go Engineer, Senior Next.js Engineer, Application Security Engineer  

---

## 1. Frontend Architecture Evaluation

### 1.1 Architecture & Component Design
The frontend is constructed with Next.js 16.2.11 using the App Router (`app/`), React 19, and Tailwind CSS. The routing model is partitioned into:
- `(auth)`: Login page and GitHub OAuth callback handlers.
- `(main)`: Protected application shell with persistent navigation, breadcrumbs, user profile drawer, and active workspace switcher.
  - `/dashboard`: High-level inventory of active and hibernated sandboxes.
  - `/projects`: Multi-tenant team projects, collaborator access controls, role assignments, and invitations.
  - `/sandboxes/[id]`: The canonical unified Sandbox Studio, integrating Monaco Editor, Live Build Logs, Interactive Terminal, Collaborators Panel, Team Activity Audit Feed, and Settings.
  - `/upload`: Streamlined repository import wizard with automated branch detection and environment variable onboarding.

### 1.2 State Management & Real-Time Invalidation
- **SWR (Stale-While-Revalidate):** Used across all main dashboard and studio views. Polling intervals dynamically adapt based on sandbox state (1.5 seconds during `BUILDING`/`QUEUED`, 5 seconds during `RUNNING`, and 1 second during live grace-period hibernation countdowns).
- **Mutations:** Optimistic UI updates with authoritative server confirmation for Git commits, sandbox renaming, project transfers, and file creation/deletion.
- **WebSocket Streaming:** Dedicated bidirectional sockets for Monaco file change synchronization and raw terminal I/O. Connections gracefully handle network blips and automatically reconnect without losing editor focus.

### 1.3 Key Architectural Corrections Made
- **Eliminated Hydration Mismatch:** Cleaned up root `layout.tsx` to prevent browser extension injections from corrupting client-side React tree rehydration.
- **Standardized Container Sizing:** Standardized the Environment Settings view to match the full-width, flexible-height viewport card pattern (`calc(100vh - 260px)`) established by the Collaborators and Code Workspace panels, removing restrictive `max-w-3xl` constraints.
- **Dynamic BFF Forwarding:** Added authoritative Next.js API routes (`/api/sandboxes` and `/api/environments`) ensuring `POST` methods forward with session cookies and authorization tokens, preventing 405 Method Not Allowed exceptions.

---

## 2. Backend Architecture Evaluation

### 2.1 Package Separation & Dependency Injection
The Go backend (`backend/internal/`) adheres to clean hexagonal architecture:
- **Transport Layer (`internal/api`):** Chi HTTP router, RFC 7807 JSON serialization, CORS middleware, session validation, and WebSocket upgrades. Transport handlers do not execute raw SQL queries; they delegate strictly to the `Store` and `Deployer` interfaces.
- **Domain & Persistence Layer (`internal/db`):** `Store` interface implemented by `GormStore`. Encapsulates PostgreSQL transactions, row-level locking, and relational constraints.
- **Orchestration Layer (`internal/orchestrator`):** Implements builder strategy pattern (Railpack, Dockerfile, Static). Manages the complete deployment pipeline from repository cloning to BuildKit image dispatch and container spawning.
- **Security & Concurrency Layer (`internal/security`, `internal/workspacelock`):** Lexical filesystem confinement, symlink escape checks, AES-256-GCM envelope encryption, and in-memory read-write locks for atomic Git operations.

### 2.2 Concurrency & Goroutine Safety
- **Workspace Locking (`workspacelock.Registry`):** Protects sandbox working directories during simultaneous user actions. Prevents simultaneous Git commits, branch checkouts, and file modifications from corrupting Git refs or staging areas.
- **Lifecycle Fencing:** Employs optimistic version fencing (`version` counter in PostgreSQL) to reject stale background expiration jobs if a user wakes or restarts a sandbox while an expiration task is in flight.
- **Race Detector Clean:** Full backend test suite passes with `go test -v -race ./...` showing zero data races across concurrent locks, HTTP handlers, and proxy middleware.

---

## 3. Intelligence Layer Evaluation

### 3.1 Deterministic Heuristics vs. AI Generation
The Intelligence layer (`backend/internal/orchestrator/analyzer.go`, `language_detector.go`, `entrypoint_resolver.go`, `internal/ai`) strictly separates deterministic facts from probabilistic AI recommendations:
1. **Deterministic Repository Heuristics:**
   - Detects package manifests (`package.json`, `requirements.txt`, `go.mod`, `pom.xml`, `Cargo.toml`).
   - Identifies framework entrypoints using file scanning (`server.js`, `index.js`, `main.py`, `app.py`, `main.go`).
   - Inspects environment requirements from `.env.example`, `.env.sample`, `.env.template`, `.env.dist`, `.env.defaults`.
2. **AI-Assisted Capabilities (Gemini 2.5 Flash):**
   - Suggests required environment variable keys and descriptions when repositories lack documentation.
   - Generates OpenAPI 3.0 specifications by analyzing source code routes.
3. **Guardrails & Fallback Behavior:**
   - AI recommendations are strictly advisory and never directly modify production configurations or container environments without explicit user confirmation.
   - If Gemini is unreachable or disabled, the system gracefully falls back to deterministic detection without blocking repository builds.

---

## 4. Database Architecture Evaluation

### 4.1 Relational Schema & Integrity (PostgreSQL)
The database schema is managed via 12 versioned migrations (`backend/migrations/`):
- `users`: Authoritative developer identity, encrypted GitHub tokens.
- `projects`: Multi-tenant collaboration boundaries.
- `project_members`: Role-based memberships (`OWNER`, `EDITOR`, `VIEWER`) with compound unique constraints (`(project_id, user_id)`).
- `repositories`: Git URLs, tracking branches, sync status.
- `sandboxes`: Core runtime entities. Enforces foreign key constraint to `projects(id)` with `ON DELETE RESTRICT` or `CASCADE`.
- `env_vars`: AES-256-GCM encrypted secrets linked to sandboxes.
- `project_sandboxes`: Authoritative 1-to-1 mapping linking sandboxes to projects.

### 4.2 Query Efficiency & N+1 Prevention
- Implemented batch lookup `GetProjectIDsForSandboxes(ids)` in `handleListSandboxes`, replacing iterative N+1 single-row queries with a single atomic SQL `WHERE sandbox_id IN (...)` query.
- Indexed all foreign keys (`project_id`, `sandbox_id`, `user_id`, `subdomain`) ensuring O(1) index scans for authorization checks.

---

## 5. Infrastructure Architecture Evaluation

### 5.1 Container Orchestration & Isolation (Docker & BuildKit)
- **Rootless & Unprivileged:** Sandboxes execute under standard unprivileged UIDs. The Docker socket (`/var/run/docker.sock`) is never mounted inside developer containers.
- **Resource Constraints:** Hard limits enforced at container creation:
  - Memory: 1024 MB maximum with swap limits.
  - CPU: 1.0 core quota (100000 µs period).
  - PIDs limit: 256 to prevent fork-bomb attacks.
- **Shared Build Cache:** BuildKit cache mounts (`type=cache,target=/root/.cache`) isolate package caches per-language while speeding up secondary rebuilds by up to 85%.

### 5.2 Gateway & Reverse Proxy (Traefik & Go Proxy)
- **Subdomain Routing:** Requests to `*.127.0.0.1.nip.io` or `*.sandbox.localhost` route through Traefik to the Go proxy.
- **Wake-on-Request:** If a container is in `HIBERNATED` state, incoming developer requests trigger singleflight container startup, holding the HTTP connection open and redirecting to the live application once healthy.
