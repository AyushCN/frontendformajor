# Database & Persistence Architecture

The platform uses a layered persistence architecture:
1. **PostgreSQL 16**: Relational storage for users, sandboxes, deployments, sidecar databases, encrypted secrets, and runtime telemetry.
2. **Redis 7**: Ephemeral hot-state store for idle activity TTLs, live resource snapshot buffers, and transient concurrency locks.
3. **Workspace Projects Store**: Fast, file-backed JSON store (`/tmp/api-sandbox-links/projects.json`) for collaborative multi-tenancy and project scoping.

---

## 1. PostgreSQL Schema & Migrations

PostgreSQL is accessed via GORM in the Go backend. Database migrations are strictly managed through sequential SQL files located in [`backend/migrations/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/migrations):

- `0001_init.up.sql`: Initial schema (users, repositories, sandboxes, env vars, databases, deployments, snapshots, audit log).
- `0002_snapshot_dedup.up.sql`: Unique constraint on `resource_snapshots(sandbox_id, recorded_at)` ensuring idempotent 10s metric flushes.
- `0003_lifetime_starts_on_activation.up.sql`: Adds `sandboxes.activated_at` to ensure sandbox lifetime begins only when status transitions to `running`.
- `0004_deployment_telemetry.up.sql`: Adds deployment duration (`build_duration_ms`) and artifact size (`image_size_bytes`) telemetry.
- `0005_resettable_hibernated_expiration.up.sql`: Adds hibernation timestamp tracking to freeze expiration during dormancy.
- `0006_projects_collaboration.up.sql`: Relational tables for workspace projects, member permissions (`OWNER`, `EDITOR`, `VIEWER`), and invitations.
- `0007_default_project_unique_and_cow.up.sql`: Default workspace constraint and Copy-on-Write sandbox forking lineage.
- `0008_runtime_telemetry.up.sql`: Runtime observability on `deployments` (`requested_runtime`, `resolved_runtime`, `runtime_artifact_id`, `provisioning_status`, `build_failure_reason`).
- `0009_sandbox_dev_mode.up.sql`: Adds `execution_mode` to `sandboxes` (`preview` vs `development` mode for Monaco live hot-reloading).
- `0010_secure_env_vars.up.sql`: Dedicated `env_vars` table for AES-256-GCM encrypted secrets at rest with `(sandbox_id, key)` unique constraint.
- `0011_consistent_sandbox_naming.up.sql`: Canonical `name` (display name) and `slug` (URL-safe routing identifier) columns on `sandboxes`.
- `0012_sandbox_project_association_integrity.up.sql`: Authoritative `project_sandboxes` mapping table with foreign keys ensuring strict 1-to-1 project integrity.

### Running Migrations

Migrations execute automatically when the backend API server starts. You can also run them manually using the dedicated migration CLI:

```bash
cd backend
go run ./cmd/migrate up    # Apply pending migrations
go run ./cmd/migrate down  # Roll back the most recent migration batch
```

### Local Development Connection
The Postgres container runs on host port **`5434`** (mapped to container port `5432` in `docker-compose.dev.yml` to prevent local port conflicts):
```
postgres://user:pass@localhost:5434/sandbox_platform?sslmode=disable
```

---

## 2. Relational Table Schema Reference

### Table Overviews

| Table | Primary Purpose | Key Columns & Indexes |
|---|---|---|
| `users` | Authenticated developers | `id uuid pk`, `github_id bigint unique`, `username`, `email`, `github_token bytea` (AES-256-GCM encrypted) |
| `repositories` | Registered GitHub repositories | `id uuid pk`, `user_id fk`, `github_url`, `default_branch`, `unique(user_id, github_url)` |
| `sandboxes` | Container environments | `id uuid pk`, `subdomain unique`, `status`, `container_id`, `internal_port`, `idle_timeout_seconds`, `activated_at`, `expires_at`, `build_config jsonb` |
| `sandbox_env_vars` | Decrypted runtime variables | `id uuid pk`, `sandbox_id fk`, `key`, `value bytea` (encrypted), `unique(sandbox_id, key)` |
| `sandbox_databases` | Per-sandbox sidecar DBs | `id uuid pk`, `sandbox_id fk`, `engine` (`postgres\|mysql`), `container_id`, `connection_url bytea` (encrypted) |
| `sandbox_lifetime_warnings` | Deduplicated alert thresholds | `id uuid pk`, `sandbox_id fk`, `threshold` (`24h\|1h\|5m`), `sent_at`, `unique(sandbox_id, threshold)` |
| `deployments` | Build history & runtime telemetry | `id uuid pk`, `sandbox_id fk`, `trigger`, `status`, `log_excerpt`, `build_duration_ms`, `image_size_bytes`, `requested_runtime`, `resolved_runtime`, `runtime_artifact_id`, `provisioning_status`, `build_failure_reason` |
| `resource_snapshots` | 10s container metric samples | `id bigint pk`, `sandbox_id fk`, `cpu_percent`, `memory_used_bytes`, `recorded_at`, `unique(sandbox_id, recorded_at)` |
| `file_edits` | In-browser IDE change audits | `id uuid pk`, `sandbox_id fk`, `file_path`, `edited_by fk`, `edited_at`, `pushed_to_github` |

### Runtime Telemetry Columns (`deployments`)

Migration `0008` adds five key columns to ensure complete observability of runtime provisioning and build caching:

| Column | Type | Description | Example Values |
|---|---|---|---|
| `requested_runtime` | `TEXT` | Raw requested language and version requirement from user or manifest | `python:3.11`, `node:22`, `python:3.5` |
| `resolved_runtime` | `TEXT` | Canonical resolved exact runtime version | `python 3.11.9`, `node 22.14.0`, `python 3.5.10` |
| `runtime_artifact_id` | `TEXT` | 6-factor deterministic SHA-256 cache identity | `asl-runtime-python-3.11.9-amd64-a1b2c3d4` |
| `provisioning_status` | `TEXT` | Provisioning lifecycle outcome | `VERSION_SUPPORTED`, `VERSION_PROVISIONING_REQUIRED`, `VERSION_UNSUPPORTED` |
| `build_failure_reason` | `TEXT` | Categorized build or provisioning failure explanation | `unsupported_legacy_runtime`, `dependency_incompatible` |

> [!NOTE]
> PostgreSQL is the **authoritative system of record** for all runtime resolution and build state. While Redis is used for transient locks and single-flight coordination, runtime availability and build telemetry are always durably persisted in Postgres.

### GORM Table-Name Mappings
GORM pluralizes Go structs unless explicitly overridden via `TableName()`. The following mappings are configured in [`backend/internal/db/models.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/db/models.go):

| Go Model Struct | Physical Database Table |
|---|---|
| `EnvVar` | `sandbox_env_vars` |
| `LifetimeWarning` | `sandbox_lifetime_warnings` |
| `Repository` | `repositories` |
| `SandboxDatabase` | `sandbox_databases` |
| `Deployment` | `deployments` |

---

## 3. Lifetime Accounting (`activated_at`)

A sandbox's expiration deadline is a guarantee of usable uptime. Build times—which can take 1 to 3 minutes for large repositories—must **never** consume the user's allocated lifetime.

1. **Creation**: When a sandbox is initialized, `expires_at` is provisionally set to `created_at + lifetime_seconds` as a fallback timeout in case the build hangs.
2. **Activation**: The moment the sandbox successfully builds and its container reaches `running`, the orchestrator executes an atomic conditional update:
   ```sql
   UPDATE sandboxes
      SET status = 'running',
          activated_at = NOW(),
          expires_at = NOW() + (lifetime_seconds || ' seconds')::interval
    WHERE id = $1 AND activated_at IS NULL;
   ```
3. **Idempotency**: The `activated_at IS NULL` condition ensures that subsequent redeployments or container restarts do not improperly reset the original lifetime expiration.

---

## 4. Redis Hot-State Storage

Redis operates strictly as an ephemeral cache and signaling bus; no permanent data relies on Redis:

| Key Pattern | Data Type | TTL | Purpose |
|---|---|---|---|
| `lr:<sandbox_id>` | String timestamp | `idle_timeout_seconds` (default 900s) | Refreshed on every HTTP request by the Gateway `:8090`. Expiration triggers the idle reaper to hibernate the container. |
| `stats:latest:<sandbox_id>` | JSON object | `10s` | Buffer written by the real-time `docker stats` collector. Flushed to Postgres `resource_snapshots` every 10s by `StatsFlushWorker`. |

---

## 5. Workspace Projects Data Store

Workspace Projects, collaborative member roles, and project-to-sandbox associations are managed through a high-performance JSON store at `/tmp/api-sandbox-links/projects.json`.

### Project Entity Structure
```json
{
  "id": "proj-microservices",
  "name": "Payment Microservices",
  "description": "Internal payment APIs and webhook testing",
  "owner": "AyushCN",
  "collaborators": [
    {
      "id": "collab-1",
      "userId": "AyushCN",
      "username": "AyushCN",
      "email": "ayush@example.com",
      "role": "OWNER"
    }
  ],
  "repositories": [
    {
      "id": "repo-1",
      "name": "payment-api",
      "url": "https://github.com/org/payment-api",
      "defaultBranch": "main"
    }
  ],
  "sandboxIds": [
    "0d9a52df-fe54-45dc-8b73-d4b936f2e55b"
  ],
  "createdAt": "2026-11-15T06:00:00.000Z"
}
```

### Multi-Tenancy Scoping (`filterSandboxesForProject`)
1. **The Default Project (`default-project`)**: Captures all sandboxes not explicitly bound to any custom workspace project.
2. **Custom Projects**: Only surface sandboxes whose IDs are explicitly recorded in `project.sandboxIds`.
3. **Automatic Persistence**: Changes made via the UI (creating projects, inviting members, linking sandboxes) immediately sync to disk with atomic write locks.
