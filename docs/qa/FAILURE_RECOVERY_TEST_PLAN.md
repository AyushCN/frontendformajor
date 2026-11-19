# API Sandbox Links — Failure & Chaos Recovery Test Plan

> **Document Version:** 1.0.0  
> **Testing Scope:** Fault Injection, Dependency Failure Resilience & Automatic State Recovery  

---

## 1. Chaos Engineering Objectives

A cloud development platform running ephemeral Docker containers and managing multi-tenant databases will experience unpredictable infrastructure failures in production. 

This test plan defines structured experiments to verify that:
1. **PostgreSQL & Redis Failures** do not corrupt sandbox ownership or leave dangling resources.
2. **Docker & BuildKit Outages** do not hang background workers or leave zombie containers.
3. **Backend Restarts & Network Partitions** recover cleanly without data loss or stuck states.
4. **All Operations are Idempotent** and failures produce actionable error feedback for the user.

---

## 2. Dependency Failure Injection Matrix

| Target Dependency | Fault Injection Mechanism | System Invariant Under Test | Verifiable Recovery Criteria | Severity |
| :--- | :--- | :--- | :--- | :--- |
| **PostgreSQL** | `systemctl stop postgresql` during active sandbox creation. | ACID transaction integrity. Quota locks. | API returns HTTP 503 / 500; zero orphan rows in `sandboxes` or `project_sandboxes`. Once DB returns, API recovers immediately. | **P0** |
| **Redis** | `redis-cli shutdown` while containers are running. | Session persistence and authoritative state. | PostgreSQL remains the source of truth for sandboxes and users. In-flight operations degrade gracefully; session cache warms up on reconnect. | **P1** |
| **Docker Daemon** | `systemctl kill -s SIGKILL docker` during container launch. | Clean error reporting, no leaked metadata. | Backend marks deployment as `FAILED`; updates sandbox status to `FAILED`. When Docker restarts, health monitor reconciles state without duplicate containers. | **P0** |
| **BuildKit Daemon** | Kill BuildKit container/socket during image compile. | Build queue resilience. | Deployer catches timeout/socket error; logs failure to deployment history; transitions deployment to `FAILED`; cleans build context. | **P1** |
| **Traefik Proxy** | Terminate Traefik container while gateway is running. | Upstream gateway buffering. | Preview traffic temporarily unavailable (502 Bad Gateway at edge); restarting Traefik restores dynamic routing automatically via Docker provider. | **P2** |
| **GitHub OAuth API**| Block outbound network to `github.com` via `iptables`. | Graceful authentication failure. | Login attempt returns clean HTTP 502/504 with "GitHub authentication service temporarily unavailable"; no application crash. | **P1** |
| **Network Partition**| Inject 100% packet drop on preview domain port (:8090). | Single-flight wake timeout defense. | Requests held up to 90s wake timeout, then return HTTP 504 Gateway Timeout. No goroutine leak or unbounded memory growth. | **P1** |
| **Go Backend Process**| `kill -9` Go backend during Monaco file save or commit. | Workspace consistency & atomic locks. | Unfinished file write uses temp file rename pattern; existing file not truncated or corrupted. On restart, Server cleans stale locks. | **P0** |
| **Client Disconnect** | Client terminates TCP connection during active transfer. | Database transaction atomicity. | Rollback executes on context cancellation; sandbox remains safely in source project with existing membership intact. | **P1** |

---

## 3. Detailed Failure Scenarios & Recovery Procedures

### FAIL-01: Backend Crash During Multi-Step Sandbox Deployment
- **Hypothesis:** If the Go backend server is abruptly killed (`SIGKILL`) while a sandbox is in `BUILDING` status, restarting the backend will safely reconcile the state and avoid orphan containers or hung builds.
- **Injection Steps:**
  1. Trigger new sandbox creation from a large repository.
  2. Monitor build progress until status enters `BUILDING`.
  3. Send `SIGKILL` to the backend process.
  4. Wait 15 seconds; start backend process again.
- **Verification:**
  1. Inspect status of the interrupted sandbox via `GET /api/sandboxes/{id}`.
  2. The sandbox must be in `FAILED` status or resume safely; it must **not** remain stuck in `BUILDING` indefinitely.
  3. No dangling unnamed build containers remain running on Docker host.

### FAIL-02: Simultaneous Database Disconnect During Sandbox Transfer
- **Hypothesis:** If PostgreSQL connection is severed between updating `project_sandboxes` and recording the audit log, the entire transaction rolls back cleanly.
- **Injection Steps:**
  1. Initiate sandbox transfer from Project Alpha to Project Beta.
  2. Simulate network partition to PostgreSQL port `5432` during transaction execution.
- **Verification:**
  1. Database transaction rolls back completely.
  2. Query `SELECT project_id FROM project_sandboxes WHERE sandbox_id = ...` returns Project Alpha.
  3. Sandbox does not appear in Project Beta; user receives clear error toast.

### FAIL-03: Sudden Host Reboot / Docker Engine Power Cycle
- **Hypothesis:** Following a sudden host machine crash or reboot, all previously `RUNNING` sandboxes are cleanly marked as `STOPPED` or `HIBERNATED`, and wake-on-request correctly starts them when visited.
- **Injection Steps:**
  1. Launch 3 sandboxes in `RUNNING` status.
  2. Restart the Docker daemon (`sudo systemctl restart docker`).
  3. Check sandbox database status and send HTTP request to each preview URL.
- **Verification:**
  1. Gateway catches HTTP traffic to preview URLs.
  2. Gateway detects container is stopped, invokes `docker start`, and restores traffic flow.
  3. Sandboxes transition back to `RUNNING` cleanly.

### FAIL-04: Stale Expiry Job Racing Against Awake Sandbox
- **Hypothesis:** An Expiry Reaper job scheduled while a sandbox was hibernated must **not** destroy a sandbox that was re-awakened and returned to `RUNNING`.
- **Injection Steps:**
  1. Sandbox hibernates; `expires_at` is set.
  2. User sends HTTP request to wake sandbox; status becomes `RUNNING`, `lifecycle_version` increments.
  3. Trigger an older queued expiry execution with the previous `lifecycle_version`.
- **Verification:**
  1. The optimistic locking clause `WHERE id = ... AND lifecycle_version = ...` fails to match.
  2. Expiry Reaper skips destruction; sandbox remains safely `RUNNING`.
