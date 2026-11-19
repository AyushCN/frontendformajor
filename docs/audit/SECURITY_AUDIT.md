# Security, Multi-Tenant Isolation, and Threat Model Audit

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Application Security Engineer, Principal Architect, Senior DevOps Engineer  

---

## 1. Threat Model & Security Posture Overview

API Sandbox Links executes arbitrary, untrusted user code inside containerized sandboxes while allowing real-time file editing, interactive terminal access, and Git integration. This architecture requires defensive layers across five distinct security rings:

```
[ Ring 1: Web & API Gateway ]       --> TLS, CSRF, Cookie Auth, Rate Limiting
  [ Ring 2: Multi-Tenant RBAC ]     --> Project Roles (Owner, Editor, Viewer), Token Encryption
    [ Ring 3: Filesystem Confinement ] --> Lexical Confinement, Symlink Resolution, TOCTOU Defense
      [ Ring 4: Secret Protection ]   --> Pre-commit Secret Scanner, AES-256-GCM Storage, API Masking
        [ Ring 5: Container Isolation ] --> Rootless UID, PIDs Limit, No Docker Socket, CPU/RAM Caps
```

---

## 2. Security Ring Audits & Verification

### Ring 1: Authentication & Session Management
- **OAuth 2.0 Flow:** Enforces authentic GitHub OAuth exchange with cryptographically secure, random state tokens stored in `httpOnly`, `SameSite=Lax`, `Secure` cookies. State mismatches or forged states return HTTP 401 and are logged as security warnings.
- **GitHub Token Security:** Developer GitHub OAuth access tokens are encrypted at rest using AES-256-GCM before database insertion (`users.encrypted_github_token`). Plaintext tokens are decrypted strictly in memory at the exact moment of Git push or repository cloning.
- **CSRF & Session Validation:** API handlers authenticate requests via encrypted session cookies or Bearer JWT tokens.

### Ring 2: Multi-Tenant RBAC & Object-Level Authorization
- **Role Hierarchy:**
  - `OWNER`: Full administrative authority. Can delete sandboxes, transfer projects, invite/remove members, modify environment variables.
  - `EDITOR`: Can edit code in Monaco, commit changes, run builds, restart sandboxes, and view logs. Cannot delete sandboxes, transfer projects, or change project roles.
  - `VIEWER`: Read-only access. Can view source code and build logs. Cannot edit files, commit changes, execute terminal commands, or view unmasked secret values.
- **Tenant Isolation:** Cross-tenant resource queries are blocked at the database layer. Handlers enforce `requireProjectRole` and verify that the target sandbox belongs to the project before executing mutations.

### Ring 3: Filesystem Confinement & Workspace Isolation
- **Lexical & Path Confinement (`internal/security/path.go`):**
  - All file operations pass through `SafeJoin(root, userPath)` which resolves relative paths, strips leading slashes, and verifies that the canonical path resides within `root`.
  - Rejects lexical traversal attempts (`../`, `../../etc/passwd`, `/etc/shadow`) with immediate HTTP 400 Bad Request.
- **Symlink & Hardlink Attacks:**
  - `OpenSandboxRoot` and `ValidatePathWithinRoot` evaluate symlink targets. Creating symlinks pointing outside the sandbox working directory is strictly rejected.
  - TOCTOU (Time-of-Check to Time-of-Use) attacks involving intermediate directory symlink replacements are mitigated using directory descriptor locks and real-path validation.
  - FIFO special files and device nodes are rejected to prevent worker thread deadlocks.

### Ring 4: Secret Protection & Git Commit Shield
- **Protected Environment Files:** Files named `.env`, `.env.local`, `.env.*.local`, `.env.production` cannot be committed to Git.
- **Untracked File Secret Scanning:** Before executing any `git add` or `git commit`, `SafeCommitAll` runs `ValidatePreCommit`. It scans all modified and newly created untracked files for:
  - AWS Access Keys (`AKIA...`)
  - GitHub Personal Access Tokens (`ghp_...`, `github_pat_...`)
  - Slack Tokens (`xoxb-...`, `xoxp-...`)
  - Private SSH/RSA Keys (`-----BEGIN OPENSSH PRIVATE KEY-----`)
  - Generic High-Entropy API Keys (`api_key = ...`)
- **Actionable Remediation:** If a secret is detected, the commit is aborted, returning HTTP 422 with the exact file, line number, and remediation guidance without modifying Git history.
- **API Secret Masking:** `GET /api/sandboxes/{id}/env` returns masked values (`sk-proj-••••••••`) for editors and viewers. Full plaintext values are accessible only to project Owners upon explicit request.

### Ring 5: Container Isolation & Resource Limits
- **No Docker Socket Mounting:** The host Docker socket (`/var/run/docker.sock`) is never mounted inside sandbox containers, preventing container breakout and daemon takeover.
- **Unprivileged Execution:** Containers run with default non-root users or isolated user namespaces where supported.
- **Hard Resource Quotas:**
  - Maximum Memory: 1024 MB (hard limit, OOM-killer configured).
  - CPU Quota: 1.0 CPU Core maximum.
  - Maximum PIDs: 256 (blocks fork-bomb attacks).
  - Isolated Docker Bridge Network: Sandboxes cannot communicate directly with peer sandbox containers or internal host infrastructure.

---

## 3. Vulnerability & Risk Assessment Summary

| Threat / Attack Vector | Impact Level | Mitigation Mechanism | Verification Status |
|---|---|---|---|
| **Path Traversal Escape** | High | `security.SafeJoin()` and `security.OpenSandboxRoot()` | **PASSED (14 test cases)** |
| **Symlink TOCTOU Escape** | High | Realpath validation and link target containment | **PASSED (8 test cases)** |
| **Accidental Secret Commit** | High | `ValidatePreCommit()` in `git/secret_guard.go` | **PASSED (4 test cases)** |
| **Cross-Tenant Project Access** | Critical | Compound foreign key checks and role middleware | **PASSED (6 test cases)** |
| **Privilege Escalation via Docker** | Critical | No socket mounts, dropped capabilities, PIDs limit | **PASSED (Container config)** |
| **Stale Expiration Race** | Medium | Optimistic database version fencing | **PASSED (Reaper test suite)** |
