# API Sandbox Links — Feature Test Matrix

> **Document Version:** 1.0.0  
> **Testing Scope:** 4-Dimensional Verification (Normal, Invalid, Boundary, Chaos/Failure) across all 21 Platform Modules  

---

## 1. Test Methodology

Every functional module is evaluated across four distinct test dimensions:
- **Level 1 — Normal Behavior (Happy Path):** Valid inputs, standard operational conditions, expected workflows.
- **Level 2 — Invalid Input & Negative Testing:** Malformed payloads, unauthorized actions, validation rejections.
- **Level 3 — Boundary & Concurrency:** Maximum thresholds, resource quotas, race conditions, edge-case syntax.
- **Level 4 — Failure & Chaos Recovery:** Process crashes, network partitions, container kills, disk exhaustion.

---

## 2. Master Feature Test Matrix

| Module ID | Module Name | Level 1: Normal Behavior | Level 2: Invalid Input | Level 3: Boundary Conditions | Level 4: Failure & Recovery |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **MOD-01** | **Authentication** | GitHub OAuth login creates session cookie; DevLogin works when enabled; `/api/me` returns user identity. | Invalid OAuth state/code rejected (401); malformed Bearer token rejected; expired session cookie denied. | Rapid repeated logins; concurrent auth requests from same browser; long usernames (100+ chars). | GitHub OAuth outage simulated; Redis session store killed; backend recovers session from DB fallback. |
| **MOD-02** | **Users & Profile** | User can view account info, list recent activities, and query user search by prefix. | Searching with empty query or SQL injection strings (`' OR 1=1--`) safely escaped and sanitized. | Search query with Unicode emojis, Asian script, and 500-char length strings; pagination boundaries. | User record marked deleted while session active; verifies immediate session termination. |
| **MOD-03** | **Projects & RBAC** | Create project; assign roles (`OWNER`, `EDITOR`, `VIEWER`); list projects; delete empty project. | Non-owner attempting to delete project returns 403; Viewer attempting to rename project returns 403. | Project with 100+ members; project name with 255 chars; special symbols (`/`, `#`, `&`, `?`). | DB disconnect during project deletion; rollback prevents orphaned projects or dangling memberships. |
| **MOD-04** | **Repositories** | Link public GitHub repo; link private repo with OAuth token; clone into local workspace. | Inaccessible private repo returns clear diagnostic (404/403); malformed Git URL rejected. | Monorepo root directory targeting; repository with 5,000 files; submodules handling. | Git clone interrupted midway; workspace directory cleanly wiped, no partial repository lock left. |
| **MOD-05** | **Sandbox Creation** | Create sandbox with valid repo, branch, display name; links to target project; status `QUEUED`. | Conflicting `project_id` and `projectId` returns 400; non-existent project returns 404. | Concurrent creation hitting `max_active_sandboxes` limit; 100-char sandbox name normalized to valid slug. | Docker daemon down during creation; sandbox created in `FAILED` status with descriptive error; no leaked DB row. |
| **MOD-06** | **Builders & Railpack** | Detects Node.js, Python, Go, Java; compiles via Railpack; falls back to Dockerfile when present. | Missing start command on unsupported stack shows actionable configuration modal (not silent crash). | Large build context (>1GB); deep dependency trees (500+ npm packages); multi-stage Docker builds. | BuildKit process killed during image compile; deployment marked `FAILED`; build logs stream error. |
| **MOD-07** | **Container Runtime** | Spawns container with 512MB RAM, 0.75 CPU, 256 PIDs, `CapDrop ALL`, `CapAdd NET_BIND_SERVICE`. | Container attempting to write to read-only host mounts or run setuid binary blocked by kernel. | Memory bomb test (app attempts 1GB allocation) cleanly killed by Docker OOM without host crash. | Container process dies (SIGKILL); orchestrator detects unexpected exit; status transitions to `FAILED`. |
| **MOD-08** | **Live Editor (Monaco)** | Open file; modify code; auto-save/Ctrl+S writes to host workspace; changes reflected in container. | Path traversal (`../../etc/passwd`, `\0`, absolute paths) rejected with HTTP 400 Bad Request. | 5MB source file editing; 10 rapid concurrent file saves to same file handled by `workspacelock`. | File save while disk is 100% full; write error reported gracefully; original file content preserved. |
| **MOD-09** | **Terminal & Shell** | Interactive WebSocket terminal connects to container `/bin/sh` or `/bin/bash`; streams commands. | Unauthenticated connection rejected (401); Viewer role connecting in write mode blocked (403). | Rapid terminal resizing; high-frequency stdout flood (e.g. `cat /dev/urandom`); UTF-8 character render. | Container stops while terminal open; WebSocket closes cleanly with 1000/1006 code and UI alert. |
| **MOD-10** | **Logging Subsystem** | BuildKit logs stream to terminal; container stdout/stderr stream live via WebSocket. | Connecting to log stream of non-existent sandbox returns 404; unauthorized user returns 403. | Log stream generating 10,000 lines/sec; ensures frontend XTerm buffer does not freeze browser. | WebSocket drops due to network hiccup; exponential backoff reconnects and resumes log stream. |
| **MOD-11** | **Preview & Gateway** | Navigating to `<subdomain>.sandbox.localhost` proxies to container internal port; HTTP 200 returned. | Requesting non-existent subdomain returns 404 with custom branded error page. | High request volume (500 RPS) to live sandbox; path routing with query parameters, cookies, and CORS. | Container responds with 502/503; gateway returns clean downstream diagnostic; logs access attempt. |
| **MOD-12** | **Git Operations** | View git status; switch branches; commit changes; push to remote repository with user credentials. | Secret in code (`AWS_SECRET_KEY`) or `.env` file blocked by Secret Guard pre-commit validation. | Merge conflict resolution; branch names with slashes (`feature/api-v2`); empty commit message. | Remote Git server unreachable during push; local commit preserved, push error shown in UI. |
| **MOD-13** | **Env Vars & Secrets** | Upload `.env` file; add variable; secrets encrypted with AES-256-GCM; injected into container on boot. | Malformed `.env` syntax parsed cleanly or rejected with line number; duplicate keys deduplicated. | 100 environment variables; 4KB secret values; keys with underscores and numbers (`VITE_API_URL_2`). | Backend key rotation executed; secrets re-encrypted without downtime; container restart injects new keys. |
| **MOD-14** | **Collaboration** | Owner invites Editor via email; Editor accepts; gets edit access; Activity dashboard logs events. | Non-member cannot access sandbox (404/403); Viewer cannot save files or commit changes. | Concurrent edits by 3 collaborators on different files; permission changes take effect immediately. | Inviter revokes invite while invitee clicks accept; transaction rejects acceptance safely. |
| **MOD-15** | **Sandbox Transfer** | Transfer sandbox to destination project; updates association in single atomic transaction. | Transfer to project where user is only a Viewer rejected with 403; transfer to self is idempotent no-op. | Transferring sandbox while container is actively running; verifies preview routing remains intact. | DB failure during transfer; rollback ensures sandbox does not vanish or link to multiple projects. |
| **MOD-16** | **Hibernation** | Container idle for 15m (no HTTP requests, no terminal/editor activity) stops; status `HIBERNATED`. | Inactive sandbox cannot hibernate if active long-running operation (build, git) is registered in Redis. | Idle threshold configured to 60s for testing; verifies exact timer expiration window. | Docker stop fails; orchestrator retries; sandbox remains in consistent state; logs error. |
| **MOD-17** | **Wake on Request** | HTTP request to hibernated sandbox pauses, starts container, awaits readiness, proxies HTTP request. | Request to expired/deleted sandbox does not trigger wake; returns 410 Gone / 404 Not Found. | 50 simultaneous HTTP requests hit hibernated sandbox at same millisecond; single-flight lock wakes once. | Application fails to become healthy within 90s; gateway aborts, returns 504 Gateway Timeout. |
| **MOD-18** | **Expiration & Cleanup**| Sliding countdown expires; Expiry Reaper sweeps containers, networks, volumes; leaves tombstone row. | Waking a hibernated sandbox invalidates previous expiry countdown; stale job must not kill alive box. | Expiration set to 0 or negative duration rejected; workspace retention period (7 days) honored. | Container deletion partially fails; Expiry Reaper retries on next tick until resources cleanly swept. |
| **MOD-19** | **Sidecar Databases** | Provision PostgreSQL / Redis sidecar container on isolated network; injects `DATABASE_URL`. | Port collision or invalid engine configuration handled gracefully during initialization. | High connection load to sidecar database; database data persisted across sandbox restarts. | Sidecar crashes; health monitor detects failure and restarts sidecar container. |
| **MOD-20** | **Admin & Quotas** | System enforces `max_active_sandboxes` and `max_running_sandboxes` per user/tenant across cluster. | User attempting to bypass quota via direct API call receives 429 Too Many Requests / 400 Quota Exceeded. | Boundary condition: User at limit (e.g. 5/5) destroys one sandbox and immediately creates a new one. | Advisory lock prevents race condition when two requests try to allocate the final quota slot concurrently. |
| **MOD-21** | **Background Workers** | IdleReaper, ExpiryReaper, SnapshotCleanup, WarningsWorker tick on scheduled intervals. | Malformed database records do not cause background workers to panic or crash the server. | Thousands of dormant sandboxes evaluated by reaper query in single batch using indexed queries. | Backend server restarts abruptly; workers pick up pending jobs on boot without duplicate work. |

---

## 3. Coverage Summary & Execution Progress

- **Total Functional Modules:** 21
- **Total Test Vectors:** 84 Dimension Scenarios (4 per module)
- **High-Risk Modules Prioritized for Level 3 & Level 4:**
  - `MOD-05` (Sandbox Creation & Project Association)
  - `MOD-07` (Container Isolation & Linux Capabilities)
  - `MOD-08` (Monaco Path Traversal & File Synchronization)
  - `MOD-12` (Git Pre-Commit Secret Scanning)
  - `MOD-13` (AES-256-GCM Secret Security)
  - `MOD-15` (Sandbox Transfer Transaction Integrity)
  - `MOD-16` & `MOD-17` (Hibernation, Single-Flight Wake, and Sliding Expiry Race Conditions)
