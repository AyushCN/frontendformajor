# API Sandbox Links — Production Release Checklist & Defect Register

> **Document Version:** 1.0.0  
> **Classification:** Release Gate Sign-Off & Production Readiness Certification  

---

## 1. Release Gate Criteria

The platform cannot be promoted to production if any P0 or P1 items remain unverified or open.

| Gate Level | Definition | Allowable Open Defects | Action if Violated |
| :--- | :--- | :--- | :--- |
| **P0 (Blocker)** | Cross-tenant data leakage, container breakout, plaintext secret exposure, data loss on transfer, broken auth. | **0** | **Immediate hard freeze.** No release under any circumstances. |
| **P1 (Critical)** | Sandbox creation fails, build engine breaks, terminal/editor unresponsive, stuck lifecycle timers. | **0** | **Block release** until patch verified by integration suite. |
| **P2 (Major)** | Visual layout discrepancy, delayed metric update, recoverable gateway timeout on cold boot. | ≤ 3 (with documented workarounds) | Conditional release approval by Tech Lead. |
| **P3 (Minor)** | Minor UI alignment, cosmetic tooltip typo, non-critical telemetry log format. | ≤ 10 | Release approved; backlog tickets filed. |

---

## 2. Master Verification Checklist

### Section 1: Security & Multi-Tenancy Gates
- [ ] **SEC-01:** Cross-tenant IDOR audit: User cannot view, list, or modify another user's private sandbox or files.
- [ ] **SEC-02:** Project RBAC enforced: `VIEWER` role is strictly read-only (blocked from file editing, commit, and settings changes).
- [ ] **SEC-03:** WebSocket authorization: Log and terminal streams assert token identity and close on membership revocation.
- [ ] **SEC-04:** Pre-Commit Secret Guard: Blocked from committing `.env` or detected API keys in Git repository.
- [ ] **SEC-05:** AES-256-GCM encryption verified: PostgreSQL table `env_vars` contains ciphertext only; API obfuscates secrets.
- [ ] **SEC-06:** Container containment: `CapDrop: ["ALL"]`, `CapAdd: ["NET_BIND_SERVICE"]`, `no-new-privileges: true` verified.
- [ ] **SEC-07:** Path traversal: File APIs sanitize paths (`filepath.Clean`), rejecting `..` and null bytes.

### Section 2: Core Functional Gates
- [ ] **CORE-01:** Sandbox creation in default personal project succeeds atomically.
- [ ] **CORE-02:** Sandbox creation in shared project succeeds atomically with correct project association.
- [ ] **CORE-03:** Conflicting `project_id` and `projectId` requests return HTTP 400 Bad Request.
- [ ] **CORE-04:** Monaco Editor live save: Writes to host workspace; changes immediately reflected in container bind-mount.
- [ ] **CORE-05:** Sandbox transfer: Moves sandbox between projects atomically without re-linking or modifying source repository.
- [ ] **CORE-06:** Sandbox naming: Display name, canonical slug, and preview URL subdomain generated consistently.

### Section 3: Lifecycle & Failure Recovery Gates
- [ ] **LIFE-01:** Idle Inactivity: Containers stop after idle timeout; database transitions to `HIBERNATED`.
- [ ] **LIFE-02:** Wake-on-Request: HTTP request to hibernated subdomain wakes container via single-flight lock without 502 errors.
- [ ] **LIFE-03:** Sliding Expiration: Active wake invalidates expiration countdown; old jobs cannot kill active sandbox.
- [ ] **LIFE-04:** Permanent Expiration: Containers and networks swept; workspace files preserved in cold storage for retention period.
- [ ] **FAIL-01:** Database crash during transfer or creation rolls back completely with zero orphaned records.
- [ ] **FAIL-02:** Docker daemon restart recovered by health monitor without creating duplicate containers.

---

## 3. Prioritized Defect and Risk Register

The following concrete risks and defect findings were identified during the architectural and codebase audit:

### Item 1: Unverified Auto-Staging in Git Commit Handler
- **ID:** DEF-001
- **Severity:** **P1 (High)**
- **Subsystem:** In-Browser Editor / Git
- **Source Location:** `backend/internal/api/commit_push.go:62`
- **Description:** When the user clicks "Commit Changes", `handleCommitPush` invokes `ValidatePreCommit`. However, if untracked files exist on disk that match secret patterns, but git has not staged them, an aggressive `git add -A` executed prior to validation could accidentally stage them.
- **Impact:** Risk of pushing sensitive files if user created a local `.env` inside Monaco before pre-commit validation ran.
- **Remediation:** Enforce that `ValidatePreCommit` checks both working tree status and untracked files *before* any staging occurs.

### Item 2: Long-Lived WebSocket Authorization on Membership Revocation
- **ID:** DEF-002
- **Severity:** **P1 (High)**
- **Subsystem:** WebSocket Channels
- **Source Location:** `backend/internal/api/websocket.go:45`
- **Description:** WebSocket handshake verifies user authorization when opened. If a collaborator is removed from a project while their browser tab has a live terminal or log WebSocket open, the socket connection remains active until next network reconnect.
- **Impact:** Removed collaborator could continue watching live build logs or terminal output until page reload.
- **Remediation:** Implement a periodic membership validation ticker or Redis pub/sub revocation signal to terminate active WebSockets when a user is deleted from `project_members`.

### Item 3: Gateway Cold Start Timeout for Heavy Stacks
- **ID:** DEF-003
- **Severity:** **P2 (Medium)**
- **Subsystem:** Traffic Gateway
- **Source Location:** `backend/internal/proxy/wake_middleware.go:30`
- **Description:** Wake timeout is globally set to `90 * time.Second`. Heavy enterprise stacks (Spring Boot, PyTorch, Rust compiles) can take 120s+ on cold start, causing the gateway to return HTTP 504 Gateway Timeout to the browser while the container is still initializing.
- **Impact:** False error shown to user on first visit to cold sandbox.
- **Remediation:** Make wake timeout dynamically configurable via environment variable or sandbox build settings (`boot_timeout_seconds`).

---

## 4. Final Sign-Off Certification

| Role | Sign-Off Status | Date | Notes |
| :--- | :--- | :--- | :--- |
| **Lead QA Architect** | `APPROVED FOR TEST RUN` | 2026-11-19 | Audit documentation complete; test plans and matrix delivered. |
| **Go Backend Systems Engineer** | `READY FOR QA SUITE` | 2026-11-19 | Migrations, atomic transfers, and N+1 fixes verified. |
| **Next.js Frontend Engineer** | `READY FOR QA SUITE` | 2026-11-19 | Settings tab layout, BFF routing, and modals verified. |
| **AppSec Lead** | `PENDING DEF-001/002 PATCH` | 2026-11-19 | Pre-commit secret scanning and WebSocket revocation required before release. |
