# API Sandbox Links

[![CI](https://github.com/AyushCN/frontendformajor/actions/workflows/ci.yml/badge.svg?branch=intelligence)](https://github.com/AyushCN/frontendformajor/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat&logo=go)](https://golang.org)
[![Next.js](https://img.shields.io/badge/Next.js-16.2-black?style=flat&logo=next.js)](https://nextjs.org)
[![Docker](https://img.shields.io/badge/Docker-28%2B-2496ED?style=flat&logo=docker)](https://www.docker.com)
[![Builder](https://img.shields.io/badge/Builder-Railpack%20%2B%20BuildKit-FF416C?style=flat)](https://railpack.com)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> The operating system for modern ephemeral cloud environments. Turn any GitHub repository into an isolated, time-limited, live-previewable developer sandbox in seconds.

> **Active development branch:** [`intelligence`](https://github.com/AyushCN/frontendformajor/tree/intelligence)

---

## Highlights

* 🧠 **Deterministic Runtime Resolution & Availability**: Resolves exact runtime versions (Python 3.11.9, Node 22.14.0, Go 1.24.x $\rightarrow$ 1.24.0, Java 21 $\rightarrow$ 21.0.2) across an 8-tier precedence hierarchy. Validates versions against builder compatibility, host OS/architecture (`amd64`, `arm64`), and dependency constraints into 5 discrete states (`VERSION_SUPPORTED`, `VERSION_UNAVAILABLE`, `VERSION_UNSUPPORTED`, `VERSION_INCOMPATIBLE`, `VERSION_PROVISIONING_REQUIRED`) with zero silent version drift.
* ⚡ **Shared BuildKit Runtime Cache**: 6-factor immutable artifact cache keys (`Language`, `Exact Version`, `OS`, `Architecture`, `DistributionID`, `BuilderVersion`) cached across independent sandboxes. Features thread-safe active build leasing (`AcquireLease`/`ReleaseLease`), LRU eviction preserving in-use artifacts, and byte quotas to eliminate duplicate downloads.
* 🗃️ **Isolated Package-Manager Caching**: High-performance BuildKit cache mounts (`--mount=type=cache`) for `pip`, `uv`, `npm`, `pnpm`, `yarn`, `go` (build + mod), `maven`, `gradle`, `cargo`, and `ccache` with strict sharing modes (`shared`, `locked`, `private`) and multi-tenant namespace isolation.
* 🔄 **Single-Flight Provisioning Deduplication**: Concurrent builds requesting identical missing runtimes are coalesced through Go `singleflight` groups. Utilizes cancellation-resilient background provisioning (`DoChan`) and exponential failure backoff so one client's cancellation never corrupts shared cache state.
* 🛡️ **Isolated Legacy Runtime Strategy (Python 3.5 Support)**: Automatically routes runtimes outside Railpack's native support envelope (Railpack supports Python $\ge$ 3.10) to isolated legacy Dockerfile pipelines (`python:3.5-slim` to `3.9-slim`, `node:14/16-slim`, `openjdk:11-jre-slim`) staged in isolated ephemeral directories. Reject obsolete/EOL versions (Python 3.4, 2.7, Node 10, Go 1.15, Java 8) with actionable errors without host compilation or unverified downloads.
* 📦 **Zero-Leak Sidecar Databases**: Automatic dependency scanning detects database drivers (`pg`, `mysql2`, `psycopg2`, Prisma, GORM) and provisions isolated PostgreSQL or MySQL containers on per-sandbox bridge networks.
* ⚡ **Single-Flight Wake-on-Request**: Idle containers automatically hibernate after inactivity. Inbound HTTP gateway requests (`:8090`) trigger instantaneous single-flight container wakeups with live boot log streaming.
* 👥 **Project Collaboration & RBAC**: Organize sandboxes into projects. Manage collaborator roles (`OWNER`, `EDITOR`, `VIEWER`) with backend-enforced authorization and **Copy-on-Write** isolation — an Editor never modifies the original shared sandbox.
* 💻 **In-Browser IDE & Live Telemetry**: Live stdout/stderr logs over WebSockets, xterm terminal console, Monaco code editor with save-redeploy loops, and one-click Git commits pushed to remote branches.
* 📊 **Authoritative Database Telemetry**: PostgreSQL migration `0008` persists requested runtime, resolved exact version, runtime artifact identity, provisioning status, and structured failure categories.

---

## Architecture at a Glance

```
                              Internet / Developer
                                       │
                                       ▼
                     ┌───────────────────────────────────┐
                     │   Traefik Edge Reverse Proxy      │  :80 / :443 (Wildcard *.sandbox.localhost)
                     └─────────────────┬─────────────────┘
                                       │
                    ┌──────────────────┴──────────────────┐
                    │ (Sandbox HTTP)                      │ (Console & Control Plane)
                    ▼                                     ▼
        ┌───────────────────────┐             ┌───────────────────────┐
        │  Wake Gateway (:8090) │             │   API Server (:8080)  │  chi router, WebSockets
        └───────────┬───────────┘             └───────────┬───────────┘
                    │                                     │
                    ▼                                     ├─▶ PostgreSQL (:5434 / state & telemetry)
        ┌───────────────────────┐                         ├─▶ Redis (:6380 / idle TTLs & transient locks)
        │ Live App Sandboxes    │                         ├─▶ BuildKit Daemon (:1234)
        │ sandbox_<id>          │                         ├─▶ RuntimeAvailabilityManager
        │ Sidecars (Postgres/DB)│                         │     ├─ Singleflight Deduplication
        └───────────────────────┘                         │     └─ SharedRuntimeCache (LRU + Leases)
                                                          └─▶ Docker Daemon Socket
```

### Port Allocation Reference

| Service | Port | Host Binding | Purpose |
|---|---|---|---|
| **Frontend Console** | `3000` | `127.0.0.1` | Next.js 16 App Router UI & Monaco IDE |
| **API Control Plane** | `8080` | `127.0.0.1` | REST endpoints & live WebSocket telemetry |
| **Gateway Proxy** | `8090` | `127.0.0.1` | Reverse proxy with single-flight wake-on-request |
| **Edge Router (Traefik)** | `80`, `8081` | `0.0.0.0` | `*.sandbox.localhost` edge routing & Traefik dashboard (`:8081`) |
| **PostgreSQL** | `5434` | `127.0.0.1` | Primary persistent state, telemetry, and audit logs (`user:pass`, DB `sandbox_platform`) |
| **Redis** | `6380` | `127.0.0.1` | Inactivity TTL markers (`lr:<id>`), metrics buffers, and transient coordination |
| **BuildKit Daemon** | `1234` | `127.0.0.1` | TCP build daemon for rootless layer-cached Railpack and Dockerfile builds |

---

## Runtime Provisioning & Cache Pipeline

The runtime resolution and build pipeline guarantees deterministic environments and fast builds:

```
                      Repository Deployment Request
                                     │
                                     ▼
                          RepositoryScanner / LanguageDetector
                                     │
                                     ▼
                          RuntimeResolver (8-Tier Precedence)
              Resolves canonical exact runtime (e.g. Python 3.11.9, Node 22.14.0)
                                     │
                        ┌────────────┴────────────┐
                        ▼                         ▼
            Railpack Supported?           Legacy Runtime? (e.g. Python 3.5)
           (Python >= 3.10, Node >= 18)   (Python 3.5-3.9, Node 14/16, Java 11)
                        │                         │
                        ▼                         ▼
             Strategy: railpack          Strategy: dockerfile (Legacy)
                        │                         │
                        └────────────┬────────────┘
                                     ▼
                      RuntimeAvailabilityManager
                                     │
                     ├── Cache Hit? ─┴── Cache Miss? ──┐
                     ▼                                 ▼
             Acquire Active Lease             singleflight.Group.DoChan
             (Prevent LRU Eviction)                    │
                     │                        Provision via BuildKit
                     │                        (Immutable Artifact)
                     │                                 │
                     └───────────────┬─────────────────┘
                                     ▼
                       BuildKit Build Execution
               Runtime Artifact + Isolated Package Mounts
                  (--mount=type=cache,id=asl-pip-...)
                                     │
                                     ▼
                          Release Active Lease
                                     │
                                     ▼
                      Record Telemetry in PostgreSQL
               (requested_runtime, resolved_runtime, artifact_id)
```

---

## Quick Start (Local Development)

### 1. Prerequisites
- **Docker Engine** & **Docker Compose v2** installed and running.
- **Go 1.25+** (tested against Go 1.25 and 1.26).
- **Node.js 20+** (LTS or Node 24).
- **GitHub OAuth App** with callback URL `http://localhost:8080/api/auth/callback/github` (optional for local dev auth).
- **Railpack CLI** (optional for local host inspection; production builds execute via BuildKit):
  ```bash
  curl -sSL https://railpack.com/install.sh | sh
  ```

### 2. Infrastructure Setup
```bash
# Clone the repository
git clone https://github.com/AyushCN/frontendformajor.git
cd frontendformajor
git checkout intelligence

# Configure environment variables
cp .env.example .env
# Edit .env if customizing secrets, OAuth, or ports

# Spin up Postgres (5434), Redis (6380), Traefik (80/8081), and BuildKit (1234)
docker compose -f docker-compose.dev.yml up -d
```

### 3. Run Database Migrations
Migrations run automatically on API server startup, or you can run them manually via the dedicated migration CLI:

```bash
cd backend
go run ./cmd/migrate up
cd ..
```

### 4. Backend Service
```bash
cd backend
go run ./cmd/server
```

> **Development Mode** (`APP_ENVIRONMENT=development`): DevAuth is enabled by default (`devuser`, ID `1`). GitHub OAuth credentials are optional.  
> **Production Mode** (`APP_ENVIRONMENT=production`): `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, and `GITHUB_OAUTH_CALLBACK_URL` are required at startup. DevAuth is strictly disabled.

### 5. Frontend Application
```bash
cd frontend
npm install
npm run dev
```

Open **[http://localhost:3000](http://localhost:3000)** in your browser to access the dashboard.

---

## Repository Structure

```
api-sandbox-links/
├── backend/                       # Go service (Chi, pgx, Docker Engine SDK, BuildKit)
│   ├── cmd/
│   │   ├── server/                # Application entrypoint with startup validation
│   │   └── migrate/               # Standalone database migration runner CLI
│   ├── internal/
│   │   ├── api/                   # REST routes, WebSockets, auth, CoW sandboxing
│   │   ├── config/                # Environment configuration loader and validation
│   │   ├── db/                    # Typed database queries and GORM models
│   │   ├── orchestrator/          # Container lifecycle, builders, and runtime management
│   │   │   ├── runtime_types.go   # VersionStatus, 6-factor RuntimeCacheKey, CacheMountConfig
│   │   │   ├── runtime_cache.go   # SharedRuntimeCache with thread-safe leases & LRU eviction
│   │   │   ├── runtime_availability.go # RuntimeAvailabilityManager & singleflight deduplication
│   │   │   ├── runtime_resolver.go# 8-tier precedence resolution & canonical exact versions
│   │   │   ├── legacy_runtime.go  # LegacyRuntimePolicy (Python 3.5, Node 14/16, Java 11)
│   │   │   ├── context_staging.go # Isolated staging and legacy Dockerfile generation
│   │   │   ├── build_plan.go      # ResolvedBuildPlan generation with runtime validation
│   │   │   ├── strategy_selector.go # Builder selection routing legacy runtimes
│   │   │   ├── builder_railpack.go# Railpack builder integration with BuildKit TCP
│   │   │   └── deploy.go          # Deployment execution, status transitions & telemetry
│   │   ├── proxy/                 # Gateway reverse proxy with single-flight wake logic
│   │   └── reaper/                # Idle & expiry background workers with retry-on-failure
│   └── migrations/                # Versioned SQL migrations (0001–0008)
├── frontend/                      # Next.js 16 (React 19, Tailwind CSS v4, Monaco Editor)
│   ├── app/                       # App Router pages (Dashboard, Projects, IDE, Upload)
│   ├── components/                # UI components, terminal consoles, theme toggle
│   └── lib/                       # Typed API client, auth state, theme provider
├── docs/                          # Architectural specifications, setup, and runbooks
│   ├── ARCHITECTURE.md            # System design, wake proxy, runtime & cache architecture
│   ├── SETUP.md                   # Development setup runbook, ports, and troubleshooting
│   ├── API.md                     # REST & WebSocket endpoint catalog
│   ├── DATABASE.md                # PostgreSQL schema (0001–0008) and data lifecycle
│   ├── SECURITY.md                # Threat models, gVisor containment, and cache isolation
│   ├── RUNBOOK.md                 # Production deployment and operations guide
│   └── SINGLE_TENANT_AUDIT.md     # Engineering audit and remediation log
├── traefik/                       # Traefik dynamic file configuration
├── docker-compose.dev.yml         # Development infrastructure stack
└── docker-compose.prod.yml        # Production infrastructure blueprint
```

---

## Security Design

| Property | Implementation |
|---|---|
| **No Host Runtimes** | Language runtimes are never installed on the Go backend host; all execution occurs inside container sandboxes or rootless BuildKit. |
| **No Socket Exposure** | Tenant workloads and builds never receive access to `/var/run/docker.sock`. |
| **Immutable Cache Artifacts** | Runtime artifacts in the shared cache are read-only and verified by 6-factor deterministic SHA-256 identities. |
| **Tenant Cache Isolation** | Package manager cache mounts support tenant scoping (`PackageCacheOptions.TenantID`) to prevent cross-tenant data leakage. |
| **Legacy Version Containment** | Legacy runtimes (e.g. Python 3.5) execute in isolated Dockerfile builds using official verified `-slim` base images; obsolete EOL runtimes are rejected with actionable errors. |
| **Container Hardening** | `CapDrop: ALL`, `CapAdd: NET_BIND_SERVICE`, `no-new-privileges`, per-sandbox bridge networks, optional Google **gVisor** (`runsc`). |
| **Editor CoW** | An `EDITOR` modifying a shared sandbox gets an independent fork — the original shared sandbox is never touched. |
| **Secrets Encryption** | AES-256-GCM encryption of stored tokens via `secret.Box`. |

---

## Testing & Quality Assurance

```bash
# Set GOPATH to workspace if host GOPATH is read-only
export GOPATH=$(pwd)/.gopath

# Run unit tests and race detection across orchestrator and runtime subsystems
cd backend
go test -v -count=1 -race ./internal/orchestrator/...

# Run go vet
go vet ./...

# Frontend TypeScript check and production build
cd ../frontend
npm run build
```

---

## Documentation Index

- 🛠️ **[docs/SETUP.md](docs/SETUP.md)** — Step-by-step development setup, port mapping, and debugging guide.
- 🏛️ **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — Ingress routing, wake-on-request, builder pipeline, runtime availability, and shared cache architecture.
- ⚡ **[docs/LIFECYCLE.md](docs/LIFECYCLE.md)** — Lifecycle state machine, wake/expiry race fixes, grace periods, and data retention.
- 👥 **[docs/COLLABORATION.md](docs/COLLABORATION.md)** — Project collaboration, RBAC matrix, and Copy-on-Write isolation.
- 📡 **[docs/API.md](docs/API.md)** — Complete REST & WebSocket endpoint specifications.
- 🗄️ **[docs/DATABASE.md](docs/DATABASE.md)** — PostgreSQL schema, migrations (0001–0008), and runtime telemetry persistence.
- 🔒 **[docs/SECURITY.md](docs/SECURITY.md)** — Threat model, capability dropping, runtime isolation, and gVisor sandboxing.
- 🚀 **[docs/RUNBOOK.md](docs/RUNBOOK.md)** — Production operations, systemd units, automated recovery, and TLS.
- 🔍 **[docs/SINGLE_TENANT_AUDIT.md](docs/SINGLE_TENANT_AUDIT.md)** — Engineering audit findings and production readiness remediation log.

---

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.
