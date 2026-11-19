# Environment Variable Lifecycle, Encryption, and Secret Protection Architecture

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Application Security Engineer, Principal Architect, Senior Go Engineer  

---

## 1. End-to-End Environment Variable Lifecycle

The lifecycle of an environment variable in API Sandbox Links traverses seven secure stages:

```
[ Stage 1: Detection ]
   └── Scans repo for .env.example, .env.sample, .env.template, process.env.*, os.Getenv(*)
[ Stage 2: Onboarding & Ingestion ]
   └── User uploads .env or enters key-value pairs in onboarding popup (Merge vs Replace)
[ Stage 3: Encryption at Rest ]
   └── Key-value pairs encrypted via AES-256-GCM using MASTER_ENCRYPTION_KEY -> PostgreSQL
[ Stage 4: Runtime Injection ]
   └── Decrypted strictly in-memory at container launch -> injected into Docker RunOptions.Env
[ Stage 5: In-Browser Masking ]
   └── Monaco Editor and REST API return masked values (sk-proj-••••••••) to prevent shoulder surfing
[ Stage 6: Git Commit Shield ]
   └── SafeCommitAll blocks commits containing .env or raw credential patterns
[ Stage 7: Teardown & Fork Isolation ]
   └── Container destroyed cleanly on expiration; secrets isolated across project forks
```

---

## 2. Ingestion & Onboarding Workflow

### 2.1 First-Run Automated Detection
When a user launches a sandbox:
1. `GET /api/sandboxes/{id}/env/requirements` inspects the cloned workspace.
2. It evaluates sample templates (`.env.example`, `.env.sample`, `.env.template`, `.env.defaults`, `.env.dist`).
3. If templates are missing, it scans code entrypoints (`server.js`, `main.py`, `app.py`, `main.go`) for environment lookups (`process.env.DB_PASS`, `os.Getenv("SECRET")`).
4. If missing required variables are identified and the user has not yet configured them, the **Environment Config Popup** automatically displays.

### 2.2 Upload Strategies (Merge vs. Replace)
When uploading an existing `.env` file or submitting via the UI:
- **`MERGE` (Default):** Adds newly defined keys to the sandbox. If a key already exists, updates its value. Preserves all existing keys that are not present in the uploaded file.
- **`REPLACE`:** Atomically wipes all existing environment variables for the sandbox in PostgreSQL and replaces them exclusively with the uploaded set.

---

## 3. Cryptographic Storage & Key Management

### 3.1 AES-256-GCM Envelope Encryption
All environment variable values are encrypted at rest in PostgreSQL (`env_vars` table):
- **Algorithm:** AES-256 in Galois/Counter Mode (GCM).
- **Authentication:** 128-bit authentication tag appended to ciphertext to detect tampering.
- **Nonce/IV:** Unique 96-bit cryptographically random Initialization Vector generated per record. Nonces are never reused.
- **Storage Format:** Standard base64-encoded payload containing `[12-byte IV] + [Ciphertext] + [16-byte Tag]`.

### 3.2 Key Rotation Support (`cmd/rekey`)
The platform includes an automated CLI tool `backend/cmd/rekey/main.go` that safely decrypts all secrets in PostgreSQL using an old master key and re-encrypts them with a new key in a single atomic database transaction without service interruption.

---

## 4. Runtime Injection & Container Security

1. **Transient In-Memory Decryption:** When `DeploySandbox` or `WakeSandbox` executes, the backend decrypts environment variables in RAM.
2. **Docker Env Injection:** Values are passed directly to the Docker Engine API via `container.Config.Env` (`KEY=VALUE`).
3. **No Filesystem Persistence:** Decrypted environment variables are never written as `.env` files onto host disks or mounted volumes unless explicitly requested by the developer inside Monaco.
4. **Image Layer Safety:** Secrets are injected at runtime (`docker run -e ...`) and never baked into Dockerfile build layers or BuildKit cache entries.

---

## 5. Monaco Editor & Git Commit Protection

1. **File Tree Masking:** In the Monaco file tree, protected environment files (`.env`, `.env.local`, `.env.*.local`) are flagged with a lock badge.
2. **File Read Masking:** When a developer opens a `.env` file, sensitive values are masked (`KEY=••••••••`) to protect against screen sharing and unauthorized viewing by viewers.
3. **Pre-Commit Blocking (`SafeCommitAll`):**
   - Attempting to commit any protected environment file triggers HTTP 422 `ViolationProtectedEnvFile`.
   - Attempting to commit source code containing detected credential patterns (AWS, GitHub, SSH private keys) triggers HTTP 422 `ViolationSecretInSource`.
4. **One-Click Untrack & Gitignore:** The UI provides an immediate "Untrack File" button that calls `POST /api/sandboxes/{id}/git/untrack`. This removes the file from the Git index, appends it to `.gitignore`, and preserves the local file on disk intact.
