# API Sandbox Links — End-to-End Test Plan

> **Document Version:** 1.0.0  
> **Target Scope:** Comprehensive Automated & Manual End-to-End Test Specifications  
> **Severity Levels:**  
> - **P0 (Critical / Blocker):** Security breach, cross-tenant leak, data loss, isolation escape.  
> - **P1 (High):** Major functional break, build failure, broken transfer, stuck lifecycle.  
> - **P2 (Medium):** Recoverable error, incorrect UI state, timing anomaly.  
> - **P3 (Low):** Visual blemish, minor usability defect.  

---

## Suite A: Authentication & Multi-Tenancy

### `E2E-AUTH-001`: GitHub OAuth Login & Session Establishment
- **Severity:** P0
- **Preconditions:** Backend running with valid GitHub Client ID/Secret or DevAuth enabled.
- **Execution Steps:**
  1. Navigate to `/login` and click "Sign in with GitHub".
  2. Complete GitHub OAuth authorization screen.
  3. Inspect response headers for `Set-Cookie: session_token=...; HttpOnly; SameSite=Lax; Path=/`.
  4. Call `GET /api/me`.
- **Expected Result:** HTTP 200 OK returned with user JSON (`id`, `github_username`, `avatar_url`). Redirects cleanly to `/dashboard`.
- **Evidence Requirements:** Response payload, cookie flags verification, DB query `SELECT * FROM users WHERE github_id = ...`.

### `E2E-AUTH-002`: Session Termination & Revocation
- **Severity:** P1
- **Preconditions:** Active logged-in session.
- **Execution Steps:**
  1. Send `POST /api/auth/logout`.
  2. Verify response clears session cookie (`Max-Age=0` or expired date).
  3. Immediately call `GET /api/me` and `GET /api/sandboxes`.
- **Expected Result:** `GET /api/me` returns HTTP 401 Unauthorized; browser redirects to `/login`.
- **Evidence Requirements:** HTTP headers showing cookie clearance, 401 response JSON `{"error":"unauthorized"}`.

### `E2E-AUTH-003`: Cross-Tenant Sandbox Isolation & Unauthorized Access
- **Severity:** P0 (Release Blocker)
- **Preconditions:** Two distinct user accounts: User A (`usr_alpha`) and User B (`usr_bravo`). User A owns private Sandbox `sb_private_a`.
- **Execution Steps:**
  1. Authenticate as User B.
  2. Attempt `GET /api/sandboxes/sb_private_a`.
  3. Attempt `GET /api/sandboxes/sb_private_a/files`.
  4. Attempt `PUT /api/sandboxes/sb_private_a/file` with malicious content.
  5. Attempt to establish WebSocket connection to `/api/sandboxes/sb_private_a/logs/ws`.
- **Expected Result:** Every API request returns HTTP 404 Not Found (to prevent resource enumeration) or HTTP 403 Forbidden. WebSocket handshake is rejected with 403/401. Zero file contents or metadata leaked to User B.
- **Evidence Requirements:** API error responses, audit logs confirming access rejection, WebSocket close frame.

### `E2E-AUTH-004`: Project Membership Revocation Mid-Session
- **Severity:** P0
- **Preconditions:** User A (Owner) invites User B (Editor) to Project `proj_team`. User B is viewing sandbox `sb_shared`.
- **Execution Steps:**
  1. User A removes User B from Project `proj_team` via `DELETE /api/projects/proj_team/members/usr_bravo`.
  2. User B attempts to save a file via `PUT /api/sandboxes/sb_shared/file`.
  3. User B calls `GET /api/sandboxes`.
- **Expected Result:** File save immediately rejected with HTTP 403 Forbidden. User B's sandbox list no longer includes `sb_shared`.
- **Evidence Requirements:** DB check `SELECT * FROM project_members WHERE project_id = ... AND user_id = ...`, HTTP 403 response.

---

## Suite B: Repository & Sandbox Creation

### `E2E-SB-001`: Create Sandbox in User's Default Personal Project
- **Severity:** P1
- **Preconditions:** Authenticated user with no pre-existing projects.
- **Execution Steps:**
  1. Submit `POST /api/sandboxes` with `gitUrl: "https://github.com/expressjs/express"`, `name: "Express Demo"`, and empty `projectId`.
  2. Query database for project associations.
- **Expected Result:**
  - HTTP 202 Accepted returned.
  - Automatically provisions personal default project `User's Personal Project` if missing.
  - Inserts 1 row into `project_sandboxes` linking `sandbox.id` to the default project.
  - Slug generated: `express-demo-[6hex]`.
- **Evidence Requirements:** DB check `SELECT * FROM project_sandboxes WHERE sandbox_id = ...`.

### `E2E-SB-002`: Create Sandbox in Explicit Shared Project
- **Severity:** P0
- **Preconditions:** User belongs to shared project `proj_shared` as Editor.
- **Execution Steps:**
  1. Submit `POST /api/sandboxes` with `projectId: "proj_shared"`, `gitUrl: "https://github.com/fastapi/fastapi"`, `name: "FastAPI Shared"`.
  2. Inspect response payload and database records.
- **Expected Result:**
  - Response contains `"project_id": "proj_shared"` and `"projectId": "proj_shared"`.
  - Sandbox is **not** assigned to the owner's personal project.
  - `project_sandboxes` table links sandbox to `proj_shared`.
- **Evidence Requirements:** Response JSON, DB foreign key check.

### `E2E-SB-003`: Rejection of Malformed and Conflicting Project IDs
- **Severity:** P1
- **Preconditions:** Authenticated user.
- **Execution Steps:**
  1. Submit `POST /api/sandboxes` with conflicting parameters: `project_id: "proj_a"` and `projectId: "proj_b"`.
  2. Submit `POST /api/sandboxes` with non-existent `projectId: "00000000-0000-0000-0000-000000000000"`.
  3. Submit `POST /api/sandboxes` targeting a project where user has only `VIEWER` role.
- **Expected Result:**
  - Conflicting request: HTTP 400 Bad Request `{"error":"conflicting project_id and projectId provided"}`.
  - Non-existent project: HTTP 404 Not Found `{"error":"project not found"}`.
  - Viewer request: HTTP 403 Forbidden `{"error":"insufficient project permissions"}`.
- **Evidence Requirements:** HTTP status codes and exact error envelopes.

### `E2E-SB-004`: Atomic Rollback on Quota or Orchestration Failure
- **Severity:** P0
- **Preconditions:** User has reached maximum active sandbox quota (`max_active_sandboxes = 5`).
- **Execution Steps:**
  1. Attempt to create a 6th sandbox via `POST /api/sandboxes`.
  2. Inspect database for partial record creation.
- **Expected Result:**
  - Returns HTTP 429 or 400 with "sandbox limit reached".
  - Entire transaction rolls back: zero rows inserted into `sandboxes` and zero rows in `project_sandboxes`.
- **Evidence Requirements:** DB query verifying total count before and after request remains identical.

---

## Suite C: Build System & Railpack Execution

### `E2E-BUILD-001`: Automatic Language Detection (Node.js & Python)
- **Severity:** P1
- **Preconditions:** Clean sandbox created with a Node.js repository (`package.json`) and another with Python (`requirements.txt`).
- **Execution Steps:**
  1. Trigger initial build deployment for each sandbox.
  2. Stream build logs via `/api/sandboxes/[id]/logs/ws`.
  3. Inspect deployment metadata in database.
- **Expected Result:**
  - Node.js repo detects Node runtime; executes `npm install` and start script.
  - Python repo detects Python runtime; executes `pip install -r requirements.txt`.
  - Deployment status transitions from `BUILDING` to `SUCCESS`.
  - Sandbox status becomes `RUNNING`.
- **Evidence Requirements:** Deployment log excerpt showing language detection, Docker image tags.

### `E2E-BUILD-002`: Dockerfile Precedence & Custom Build
- **Severity:** P1
- **Preconditions:** Repository containing both `package.json` and a custom `Dockerfile`.
- **Execution Steps:**
  1. Trigger build with default settings.
  2. Verify build engine selection.
- **Expected Result:** Build system prioritizes explicit `Dockerfile` over automatic Railpack heuristic; compiles image via BuildKit using Dockerfile instructions.
- **Evidence Requirements:** Build logs containing Dockerfile step outputs (`STEP 1/5: FROM ...`).

### `E2E-BUILD-003`: Build Failure & Actionable UI Diagnostics
- **Severity:** P1
- **Preconditions:** Repository with broken dependencies (e.g. invalid package version in `package.json`).
- **Execution Steps:**
  1. Trigger build.
  2. Observe deployment termination and frontend state.
- **Expected Result:**
  - Build fails gracefully without container crash.
  - Deployment status set to `FAILED` with non-empty `log_excerpt` containing the fatal error.
  - Sandbox status transitions to `FAILED`.
  - Frontend displays the "Build failed — check logs below" banner with active "Retry" button.
- **Evidence Requirements:** Frontend screenshot, deployment error log, HTTP status.

---

## Suite D: Runtime, Live Editor & File Persistence

### `E2E-RUN-001`: Monaco Edit, Host Bind-Mount & Container Sync
- **Severity:** P0
- **Preconditions:** Sandbox in `RUNNING` status with dev mode enabled.
- **Execution Steps:**
  1. Call `GET /api/sandboxes/[id]/files` to fetch file tree.
  2. Call `GET /api/sandboxes/[id]/file?path=server.js` to read source.
  3. Call `PUT /api/sandboxes/[id]/file` with updated code: `console.log("Hot Reload Verified 2026");`.
  4. Exec inside running container: `cat /workspace/server.js`.
- **Expected Result:**
  - HTTP 200 OK returned on save.
  - Host workspace file `/var/lib/sandboxes/[id]/workspace/server.js` contains the new string.
  - Container's mounted `/workspace/server.js` reflects the change immediately.
  - Application file watcher triggers hot reload without rebuilding Docker image.
- **Evidence Requirements:** Output of container `cat` command, HTTP 200 save response.

### `E2E-RUN-002`: Path Traversal Attack Prevention
- **Severity:** P0 (Security Blocker)
- **Preconditions:** Running sandbox.
- **Execution Steps:**
  1. Attempt `GET /api/sandboxes/[id]/file?path=../../../../etc/shadow`.
  2. Attempt `PUT /api/sandboxes/[id]/file` with `path: "../../../app/config.json"`.
  3. Attempt `GET /api/sandboxes/[id]/file?path=foo%00bar.txt` (null byte).
- **Expected Result:** All attempts rejected with HTTP 400 Bad Request `{"error":"invalid file path"}`. Zero host files exposed or overwritten.
- **Evidence Requirements:** Server log showing path sanitization rejection, HTTP 400 response.

### `E2E-RUN-003`: Container Isolation & Privilege Escalation Defenses
- **Severity:** P0 (Security Blocker)
- **Preconditions:** Running sandbox container.
- **Execution Steps:**
  1. Execute terminal probe inside container: `capsh --print`.
  2. Attempt raw packet capture: `tcpdump -i eth0`.
  3. Attempt to mount host filesystem: `mount -t proc none /mnt`.
  4. Attempt setuid root elevation.
- **Expected Result:**
  - Capabilities show `Current: = cap_net_bind_service+ep` (all other caps dropped).
  - Raw socket creation fails (`Operation not permitted`).
  - Mount commands fail (`Operation not permitted`).
  - Kernel flags confirm `no-new-privileges: true`.
- **Evidence Requirements:** Terminal command outputs, Docker inspect `HostConfig.CapDrop: ["ALL"]`.

---

## Suite E: Environment Variables & Secret Security

### `E2E-ENV-001`: Dotenv File Upload, AES-256-GCM Encryption & Masking
- **Severity:** P0
- **Preconditions:** Running sandbox.
- **Execution Steps:**
  1. Upload `.env` file via `POST /api/sandboxes/[id]/env/upload` containing:
     ```env
     STRIPE_SECRET_KEY=sk_test_51Mz982348923489
     DATABASE_URL=postgres://user:pass@db:5432/main
     PUBLIC_API_URL=https://api.example.com
     ```
  2. Query database table `env_vars`.
  3. Call `GET /api/sandboxes/[id]/env`.
- **Expected Result:**
  - In PostgreSQL: `encrypted_value` is binary ciphertext with unique IV/nonce; plaintext secret does not exist anywhere in database.
  - In API response: `value` is masked or redacted (`••••••••••••89`) for sensitive keys.
- **Evidence Requirements:** Raw DB dump of `env_vars` row, API JSON output.

### `E2E-ENV-002`: Git Pre-Commit Secret Guard Interception
- **Severity:** P0
- **Preconditions:** In-browser editor with modified `.env` or hardcoded secret in `app.js`.
- **Execution Steps:**
  1. Stage changes and call `POST /api/sandboxes/[id]/commit` with message `"Test commit"`.
- **Expected Result:**
  - Secret Guard runs pre-commit scan on the git index in memory.
  - Commit is blocked; HTTP 422 Unprocessable Entity or HTTP 400 returned.
  - JSON response returns `violations: [{file: ".env", type: "PROTECTED_ENV_FILE", remediation: "Untrack .env"}]`.
  - Git log confirms no new commit was created.
- **Evidence Requirements:** API error response payload, `git log -n 1` on the repository.

---

## Suite F: Collaboration, RBAC & Sandbox Transfer

### `E2E-XFER-001`: Project-to-Project Sandbox Transfer
- **Severity:** P0
- **Preconditions:** Project Alpha (`proj_alpha`) and Project Beta (`proj_beta`). User is Owner in both. Sandbox `sb_xfer` is currently in Alpha.
- **Execution Steps:**
  1. Submit `POST /api/sandboxes/sb_xfer/transfer` with `destination_project_id: "proj_beta"`.
  2. Inspect database `project_sandboxes` table.
  3. List sandboxes for Alpha and Beta.
- **Expected Result:**
  - HTTP 200 OK returned.
  - `project_sandboxes.project_id` atomically updated to `proj_beta`.
  - Sandbox disappears from Project Alpha dashboard and appears in Project Beta dashboard.
  - Source repository link in Project Alpha remains completely unaffected.
  - Workspace files, container ID, and Git history remain 100% intact.
- **Evidence Requirements:** DB query before/after, response JSON, audit log event `TRANSFER_SANDBOX`.

### `E2E-XFER-002`: Transfer Idempotency & Permission Enforcement
- **Severity:** P1
- **Preconditions:** User is Viewer in destination project `proj_gamma`.
- **Execution Steps:**
  1. Attempt transfer to `proj_gamma`.
  2. Attempt transfer to the same project the sandbox is already in (`proj_beta`).
- **Expected Result:**
  - Transfer to `proj_gamma`: Rejected with HTTP 403 Forbidden ("actor lacks editor/owner permissions in destination project").
  - Transfer to same project: Succeeds as an idempotent no-op (HTTP 200 OK, no unnecessary DB mutations).
- **Evidence Requirements:** HTTP response statuses and error messages.

---

## Suite G: Hibernation, Wake-on-Request & Expiration

### `E2E-LIFE-001`: Idle Inactivity Hibernation
- **Severity:** P1
- **Preconditions:** Running sandbox with test idle timeout configured to 30 seconds (`idle_timeout_seconds = 30`).
- **Execution Steps:**
  1. Leave sandbox untouched with no HTTP requests, terminal sessions, or file edits.
  2. Allow Idle Reaper tick to run.
  3. Inspect container state and database row.
- **Expected Result:**
  - Idle Reaper detects `last_accessed_at + 30s < now`.
  - Container is stopped gracefully (`docker stop`).
  - Database status updates to `HIBERNATED`.
  - Expiration countdown commences.
- **Evidence Requirements:** Docker container status (`Exited`), DB status `HIBERNATED`, reaper log output.

### `E2E-LIFE-002`: Single-Flight Wake on HTTP Request
- **Severity:** P0
- **Preconditions:** Sandbox in `HIBERNATED` status. Subdomain: `weather-api-1db5cc`.
- **Execution Steps:**
  1. Dispatch 20 concurrent HTTP requests simultaneously to `http://weather-api-1db5cc.sandbox.localhost:8090/health`.
  2. Monitor Gateway logs and Docker engine.
- **Expected Result:**
  - Gateway catches traffic, holds requests in flight.
  - Single-flight mutex executes `docker start` exactly once (no duplicate start errors).
  - Readiness probe confirms app port is healthy.
  - All 20 requests complete successfully with HTTP 200.
  - Sandbox status in PostgreSQL updates to `RUNNING`.
- **Evidence Requirements:** Docker start timestamps, Gateway single-flight log, client HTTP status codes.

### `E2E-LIFE-003`: Permanent Expiration & Workspace Cold Storage Retention
- **Severity:** P1
- **Preconditions:** Sandbox in `HIBERNATED` status with expired sliding window (`expires_at < now`).
- **Execution Steps:**
  1. Trigger Expiry Reaper pass.
  2. Inspect Docker engine for runtime containers and networks.
  3. Inspect PostgreSQL `sandboxes` table.
  4. Inspect host workspace directory `/var/lib/sandboxes/[id]/workspace`.
- **Expected Result:**
  - App container and sidecar containers removed (`docker rm -f`).
  - Sandbox database record status set to `EXPIRED` (tombstone).
  - Host workspace files remain preserved on disk for 7 days post-expiration.
  - UI displays the "Restore to New Sandbox" option.
- **Evidence Requirements:** `docker ps -a` confirming container deletion, DB record status, filesystem verification.
