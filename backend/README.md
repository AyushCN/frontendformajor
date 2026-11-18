# Backend: Control Plane & Gateway Proxy

[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat&logo=go)](https://golang.org)
[![Docker SDK](https://img.shields.io/badge/Docker-Engine%20SDK-2496ED?style=flat&logo=docker)](https://pkg.go.dev/github.com/docker/docker/client)
[![BuildKit](https://img.shields.io/badge/BuildKit-Rootless%20TCP-FF416C?style=flat)](https://github.com/moby/buildkit)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-336791?style=flat&logo=postgresql)](https://www.postgresql.org)
[![Redis](https://img.shields.io/badge/Redis-7-DC382D?style=flat&logo=redis)](https://redis.io)

The backend service is a compiled Go binary providing both the **REST and WebSocket control plane** (`:8080`) and the **single-flight wake-on-request gateway proxy** (`:8090`).

---

## Key Responsibilities

* 🏛️ **Two Independent Listeners**:
  * **`:8080` (Control Plane)**: Authenticated chi router servicing `/api/sandboxes`, `/api/health`, Monaco IDE file operations, Git commits, and live WebSocket telemetry.
  * **`:8090` (Gateway Proxy)**: Low-latency HTTP reverse proxy resolving `*.sandbox.localhost` subdomains, waking hibernated containers, and forwarding traffic.
* 🧠 **Deterministic Runtime Resolution & Availability**:
  * Resolves exact canonical versions (Python 3.11.9, Node 22.14.0, Go 1.24.0, Java 21.0.2) across an 8-tier precedence hierarchy.
  * Classifies runtime requests into 5 discrete states (`VERSION_SUPPORTED`, `VERSION_UNAVAILABLE`, `VERSION_UNSUPPORTED`, `VERSION_INCOMPATIBLE`, `VERSION_PROVISIONING_REQUIRED`).
  * Never silently upgrades or mutates requested versions.
* ⚡ **Shared BuildKit Runtime Cache & Package Caches**:
  * Persistent 6-factor cache keys (`Language`, `Version`, `OS`, `Arch`, `DistributionID`, `BuilderVersion`).
  * Thread-safe active build leasing (`AcquireLease`/`ReleaseLease`) preventing LRU eviction of in-use artifacts.
  * BuildKit `--mount=type=cache` package mounts for `pip`, `uv`, `npm`, `pnpm`, `yarn`, `go` (build + mod), `maven`, `gradle`, `cargo`, and `ccache` with optional tenant-scoped isolation.
* 🔄 **Single-Flight Provisioning Deduplication**:
  * Coalesces concurrent builds requesting the same missing runtime via `singleflight.Group`.
  * Employs cancellation-resilient `DoChan` so one user's cancellation never corrupts or aborts shared cache provisioning.
  * Implements exponential backoff on repeated provisioning failures.
* 🛡️ **Isolated Legacy Runtime Strategy (Python 3.5 Handling)**:
  * Automatically identifies runtimes outside Railpack's native support envelope ($\ge 3.10$).
  * Routes Python 3.5–3.9, Node 14/16, and Java 11 to isolated Dockerfile builds using official verified `-slim` base images.
  * Generates build contexts in isolated staging directories without modifying user Git repositories.
  * Actionably rejects obsolete/EOL runtimes (Python 3.4, 2.7, Node 10, Go 1.15, Java 8).
* 📦 **Automatic Sidecar Databases**:
  * Scans source code for database dependencies (`pg`, `mysql2`, `psycopg2`, Prisma, GORM).
  * Provisions isolated PostgreSQL or MySQL containers on a shared bridge network (`sandbox_<id[:8]>`).
* ⚡ **Single-Flight Container Wake-on-Request**:
  * Coalesces concurrent HTTP requests to a sleeping sandbox into a single `docker start` operation.
  * Waits for TCP readiness before proxying requests.
* 🛡️ **Container Hardening & Google gVisor**:
  * Drops all Linux capabilities (`CapDrop: ALL`), enforces `no-new-privileges`, supervises processes with `tini` init, and provides opt-in gVisor (`runsc`) user-space kernel virtualization.

---

## Directory Layout

```
backend/
├── cmd/
│   ├── server/                   # Application main entrypoint
│   └── migrate/                  # Standalone database migration CLI
├── internal/
│   ├── api/                      # chi router, middlewares, WebSocket handlers, auth
│   ├── config/                   # Environment configuration loader and validation
│   ├── db/                       # GORM models, repository interfaces, queries
│   ├── git/                      # Repository cloning, branch switching, commit & push
│   ├── orchestrator/             # Container lifecycle, builders, and runtime management
│   │   ├── runtime_types.go      # VersionStatus, 6-factor RuntimeCacheKey, CacheMountConfig
│   │   ├── runtime_cache.go      # SharedRuntimeCache with thread-safe leases & LRU eviction
│   │   ├── runtime_availability.go # RuntimeAvailabilityManager & singleflight deduplication
│   │   ├── runtime_resolver.go   # 8-tier precedence resolution & exact version mapping
│   │   ├── legacy_runtime.go     # LegacyRuntimePolicy (Python 3.5, Node 14/16, Java 11)
│   │   ├── context_staging.go    # Staging contexts & isolated legacy Dockerfiles
│   │   ├── build_plan.go         # Build planning and runtime status validation
│   │   ├── strategy_selector.go  # Builder strategy routing legacy runtimes
│   │   ├── builder_railpack.go   # Railpack builder with BuildKit layer caching
│   │   ├── builder_dockerfile.go # Native Dockerfile builder
│   │   └── deploy.go             # Deployment execution, status updates & telemetry
│   ├── proxy/                    # Ingress reverse proxy & single-flight wake logic
│   └── reaper/                   # Idle and lifetime expiration background workers
└── migrations/                   # Versioned SQL migrations (0001–0008)
```

---

## Building and Running

Because `go.mod` resides in `backend/`, compile from this directory:

```bash
cd backend
go build -o server ./cmd/server
```

Always launch the compiled binary from the **repository root** so `godotenv` loads `./.env` correctly:

```bash
cd ..
./backend/server
```

### Running Database Migrations

Migrations run automatically on server start, or you can manage them explicitly:

```bash
cd backend
go run ./cmd/migrate up    # Apply migrations 0001 through 0008
go run ./cmd/migrate down  # Roll back the latest migration batch
```

### Running Tests

```bash
# If host GOPATH is read-only:
export GOPATH=$(pwd)/.gopath

# Run orchestrator and runtime test suites with race detection
cd backend
go test -v -count=1 -race ./internal/orchestrator/...

# Run all backend unit tests
go test -race ./...

# Run code vetting
go vet ./...
```

For comprehensive integration testing with live Docker and PostgreSQL:
```bash
docker run -d --rm -p 55432:5432 -e POSTGRES_PASSWORD=pw postgres:16-alpine
ASL_TEST_DATABASE_URL='postgres://postgres:pw@localhost:55432/postgres?sslmode=disable' \
  go test -race ./internal/db/ ./internal/api/ ./internal/orchestrator/
```
