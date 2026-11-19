# Centralized Configuration Management Architecture

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Principal Software Architect, DevOps Engineer  

---

## 1. Architectural Philosophy

API Sandbox Links adheres strictly to the Twelve-Factor App methodology for configuration. All operational variables, service URLs, credentials, network ports, and timeouts are decoupled from source code and managed centrally through typed structs in `backend/internal/config/config.go`.

### Key Design Principles:
1. **Single Source of Truth:** `internal/config/config.go` defines every supported setting, default fallback, and validation rule. No subsystem or package reads `os.Getenv` directly outside of this package.
2. **Environment Separation:** Strict distinction between Development defaults (`.env.local`) and Production requirements (`.env.production`). Sensitive settings (such as `MASTER_ENCRYPTION_KEY`, `SESSION_SECRET`, and `GITHUB_CLIENT_SECRET`) require non-empty, high-entropy values in production.
3. **Fail-Fast Boot Validation:** `config.Load()` validates critical parameters at process startup. Missing required credentials cause immediate, clear startup termination rather than silent runtime crashes later.

---

## 2. Configuration Reference Matrix

| Setting Name | Type | Default Value | Description & Purpose | Environment Scope |
|---|---|---|---|---|
| `PORT` | `int` | `8080` | Main Go API and HTTP gateway listening port. | All |
| `BASE_DOMAIN` | `string` | `127.0.0.1.nip.io` | Base domain for sandbox preview URLs (`<slug>.<base_domain>`). | All |
| `DATABASE_URL` | `string` | `postgres://user:pass@localhost:5432/sandbox?sslmode=disable` | PostgreSQL connection string. | All |
| `REDIS_URL` | `string` | `redis://localhost:6379/0` | Redis connection URL for distributed sliding expiry and cache. | All |
| `MASTER_ENCRYPTION_KEY` | `string` | `(32-byte hex string)` | Root key for AES-256-GCM envelope encryption of secrets at rest. | Required in Prod |
| `SESSION_SECRET` | `string` | `(secure random)` | Secret for HMAC signing of user session cookies. | Required in Prod |
| `GITHUB_CLIENT_ID` | `string` | `""` | GitHub OAuth application client identifier. | Required for Auth |
| `GITHUB_CLIENT_SECRET` | `string` | `""` | GitHub OAuth application client secret. | Required for Auth |
| `SANDBOX_WORK_DIR` | `string` | `/tmp/sandboxes` | Base host directory storing preserved sandbox filesystems. | All |
| `DOCKER_NETWORK` | `string` | `sandbox-net` | Dedicated Docker bridge network providing container isolation. | All |
| `IDLE_TIMEOUT_SECONDS` | `int` | `900` (15m) | Inactivity threshold before a running sandbox hibernates. | All |
| `HIBERNATED_EXPIRATION_SECONDS` | `int` | `7200` (2h) | Sliding expiration countdown while hibernated before teardown. | All |
| `WORKSPACE_RETENTION_SECONDS` | `int` | `604800` (7d) | Disk retention period for workspace files after expiration. | All |
| `MAX_CONTAINER_MEMORY_MB` | `int` | `1024` | Hard memory quota enforced per sandbox container. | All |
| `MAX_CONTAINER_CPUS` | `float64`| `1.0` | Maximum CPU cores allocated per sandbox container. | All |
| `DEV_LOGIN_ENABLED` | `bool` | `false` | Enables local developer bypass endpoint; strictly rejected in Prod. | Dev Only |
| `GEMINI_API_KEY` | `string` | `""` | Optional Google AI key for semantic variable suggestions and OpenAPI. | Optional |

---

## 3. Configuration Loading & Validation Sequence

```
1. Process Startup (`cmd/server/main.go`)
   └── Call `config.Load()`
         ├── Read `.env` if present
         ├── Parse environment variables into typed `Config` struct
         ├── Apply safe defaults for unset optional settings
         ├── Validate invariant requirements:
         │     ├── Is MASTER_ENCRYPTION_KEY valid 32-byte key?
         │     ├── Is GITHUB_CLIENT_ID & SECRET set if ENV=production?
         │     └── Is SANDBOX_WORK_DIR accessible and writable?
         └── Return immutable `*Config` pointer to Server constructor
```
