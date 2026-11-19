# API Sandbox Links — Application Security & Multi-Tenant Test Plan

> **Document Version:** 1.0.0  
> **Testing Classification:** AppSec, Tenant Boundary Isolation, Container Containment & Secret Protection  

---

## 1. Threat Model & Attack Surface Overview

API Sandbox Links executes arbitrary, untrusted third-party code submitted by users inside multi-tenant infrastructure. The security perimeter must defend against:
1. **Container Escape & Kernel Exploits:** Untrusted user code attempting root privilege escalation, host filesystem access, or namespace breakout.
2. **Cross-Tenant Data Leakage (IDOR):** User A manipulating identifiers to access User B's sandboxes, files, logs, metrics, or project resources.
3. **Secret & Credential Exposure:** Decryption key exposure, `.env` file exfiltration via Git commit, leaking secrets via build logs or API responses.
4. **Denial of Service (DoS):** Fork bombs, memory exhaustion, infinite build loops, and API flood attacks designed to overwhelm host compute.

```
                           [ Threat Model Boundaries ]

 [ Public Internet ]
         │
         ▼
 ┌───────────────┐
 │ API / Gateway │ ──► [ Auth Bypass / IDOR / Parameter Tampering ]
 └───────┬───────┘
         │
         ▼
 ┌───────────────┐
 │ Host OS Node  │ ──► [ Path Traversal / Host Filesystem Tampering ]
 └───────┬───────┘
         │
         ▼
 ┌───────────────┐
 │ Docker Daemon │ ──► [ Socket Abuse / Unsafe Mounts / Daemon DoS ]
 └───────┬───────┘
         │
         ▼
 ┌───────────────┐
 │ Sandbox App   │ ──► [ CapDrop ALL / No-New-Privileges / OOM Limits ]
 └───────────────┘
```

---

## 2. Security Test Vectors & Attack Scenarios

### SEC-01: Broken Object-Level Authorization (BOLA / IDOR)

| Test ID | Attack Description | Execution Method | Expected Security Response |
| :--- | :--- | :--- | :--- |
| **SEC-BOLA-01** | Cross-tenant sandbox inspection | User A calls `GET /api/sandboxes/{sandbox_B_id}` using valid session A. | Returns `404 Not Found` (avoids confirming existence) or `403 Forbidden`. No metadata leaked. |
| **SEC-BOLA-02** | Cross-tenant Monaco file exfiltration | User A calls `GET /api/sandboxes/{sandbox_B_id}/file?path=server.js`. | Returns `404 Not Found` or `403 Forbidden`. Zero source bytes returned. |
| **SEC-BOLA-03** | Unauthorized file modification | User A sends `PUT /api/sandboxes/{sandbox_B_id}/file` with malicious payload. | Returns `403 Forbidden`. Disk write aborted; audit log records security alert. |
| **SEC-BOLA-04** | Project membership manipulation | User B (Editor) attempts `PATCH /api/projects/{id}/members/{user_a}` to elevate to Owner. | Returns `403 Forbidden`. Only existing Project Owner can reassign roles. |
| **SEC-BOLA-05** | Unauthorized sandbox transfer | Non-owner of sandbox or source project attempts `POST /api/sandboxes/{id}/transfer`. | Returns `403 Forbidden` ("actor lacks transfer authorization"). Transfer aborted. |

### SEC-02: Path Traversal & Filesystem Hardening

| Test ID | Attack Description | Execution Method | Expected Security Response |
| :--- | :--- | :--- | :--- |
| **SEC-FS-01** | Directory climbing via `..` | Send `GET /api/sandboxes/{id}/file?path=../../../../etc/passwd`. | `filepath.Clean` detects path escape; rejected with `400 Bad Request`. |
| **SEC-FS-02** | Absolute path evasion | Send `GET /api/sandboxes/{id}/file?path=/var/run/docker.sock`. | Leading `/` stripped or rejected with `400 Bad Request`. |
| **SEC-FS-03** | Null-byte file poisoning | Send `PUT /api/sandboxes/{id}/file` with path `main.js\0.png`. | String validation rejects null bytes immediately (`400 Bad Request`). |
| **SEC-FS-04** | Symlink traversal escape | Repository contains symlink pointing to `/etc/shadow` or `/var/lib/docker`. | File API verifies `os.Lstat` and canonical target remains strictly inside sandbox root. |

### SEC-03: Container Isolation & Kernel Attack Surface

| Test ID | Attack Description | Execution Method | Expected Security Response |
| :--- | :--- | :--- | :--- |
| **SEC-DOCK-01** | Linux capabilities audit | Run `capsh --print` inside application container. | Verifies all default caps dropped (`CapDrop: ["ALL"]`). Only `CAP_NET_BIND_SERVICE` retained. |
| **SEC-DOCK-02** | Privilege re-elevation prevention | Untrusted repo executes setuid root binary inside container. | Blocked by kernel flag `SecurityOpt: ["no-new-privileges:true"]`. Elevation fails. |
| **SEC-DOCK-03** | Host filesystem mount inspection | Inspect container mounts via `docker inspect`. | Workspace mount is restricted to `/var/lib/sandboxes/{id}/workspace`. No host root mounts. |
| **SEC-DOCK-04** | Docker socket access probe | Probe for `/var/run/docker.sock` from inside application container. | Socket does not exist inside container; raw Docker API calls impossible. |
| **SEC-DOCK-05** | Denial of Service: Fork bomb | Execute `:(){ :\|:& };:` inside container shell. | Kernel PID limit (`PidsLimit: 256`) caps thread creation. Host system unaffected. |
| **SEC-DOCK-06** | Denial of Service: Memory flood | Execute script allocating 2GB RAM. | Container memory limit (`Memory: 512MB`) triggers OOM killer on container without host crash. |

### SEC-04: Secret Protection & Git Exfiltration Defenses

| Test ID | Attack Description | Execution Method | Expected Security Response |
| :--- | :--- | :--- | :--- |
| **SEC-SEC-01** | Database secret dump attack | Inspect PostgreSQL table `env_vars` directly. | Plaintext values absent. Storage encrypted with AES-256-GCM using isolated master key. |
| **SEC-SEC-02** | API secret extraction | Call `GET /api/sandboxes/{id}/env`. | Sensitive keys return obfuscated strings (`••••••••••••1234`). Plaintext secrets never returned. |
| **SEC-SEC-03** | Git commit exfiltration of `.env` | User edits `.env` in editor and executes `POST /api/sandboxes/{id}/commit`. | Secret Guard blocks commit in memory (`422 Unprocessable Entity`). Git tree unchanged. |
| **SEC-SEC-04** | Git commit with high-entropy token | Source code modified to include `ghp_abcdefghijklmnopqrstuvwxyz0123456789`. | Pre-commit regex scanner flags GitHub PAT pattern; blocks commit with diagnostic error. |
| **SEC-SEC-05** | Build log secret leaking | Environment variable used in start command (e.g. `npm start --key=$API_KEY`). | Build output demuxer filters and masks known environment variable values from public log stream. |
| **SEC-SEC-06** | Sandbox fork secret leakage | User B creates a copy-on-write fork of User A's public sandbox. | Fork clones repository workspace and Git history, but excludes User A's secret environment variables. |

---

## 3. Automated Security Verification Commands

```bash
# 1. Assert Linux Capability Hardening in Docker Go Code
cd backend && go test -v -run TestSandboxIsolation ./internal/orchestrator

# 2. Run Pre-Commit Secret Guard Test Suite
cd backend && go test -v ./internal/git -run TestSecretGuard

# 3. Verify AES-256-GCM Encryption at Rest
cd backend && go test -v ./internal/security -run TestSecretBox

# 4. Run Multi-Tenant RBAC Authorization Matrix Tests
cd backend && go test -v ./internal/api -run TestProjectAssociation_MultiTenantIsolation
```
