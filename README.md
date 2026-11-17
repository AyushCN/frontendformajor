# API Sandbox Links

[![CI](https://github.com/AyushCN/frontendformajor/actions/workflows/ci.yml/badge.svg?branch=frontend)](https://github.com/AyushCN/frontendformajor/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat&logo=go)](https://golang.org)
[![Next.js](https://img.shields.io/badge/Next.js-16.2-black?style=flat&logo=next.js)](https://nextjs.org)
[![Docker](https://img.shields.io/badge/Docker-28%2B-2496ED?style=flat&logo=docker)](https://www.docker.com)
[![Builder](https://img.shields.io/badge/Builder-Railpack%20%2B%20BuildKit-FF416C?style=flat)](https://railpack.com)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> The operating system for modern ephemeral cloud environments. Turn any GitHub repository into an isolated, time-limited, live-previewable developer sandbox in seconds.

> **Active development branch:** [`frontend`](https://github.com/AyushCN/frontendformajor/tree/frontend)

---

## Highlights

* 🚀 **Deterministic Builder Engine**: Prioritizes native `Dockerfile` configurations, intelligent **Railpack** buildpack plans with **BuildKit** layer caching over TCP (`:1234`), and Nixpacks fallback.
* 📦 **Zero-Leak Sidecar Databases**: Automatic dependency scanning detects database drivers (`pg`, `mysql2`, `psycopg2`, Prisma, GORM) and provisions isolated PostgreSQL or MySQL containers on per-sandbox bridge networks.
* ⚡ **Single-Flight Wake-on-Request**: Idle containers automatically hibernate after inactivity. Inbound HTTP gateway requests (`:8090`) trigger instantaneous single-flight container wakeups with live boot log streaming.
* 👥 **Project Collaboration & RBAC**: Organize sandboxes into projects. Manage collaborator roles (`OWNER`, `EDITOR`, `VIEWER`) with backend-enforced authorization and **Copy-on-Write** isolation — an Editor never modifies the original shared sandbox.
* 💻 **In-Browser IDE & Live Telemetry**: Live stdout/stderr logs over WebSockets, xterm terminal console, Monaco code editor with save-redeploy loops, and one-click Git commits pushed to remote branches.
* 🛡️ **Production-Grade Security**: Capability dropping (`CapDrop: ALL`), `no-new-privileges`, rootless BuildKit builds, AES-256-GCM secret encryption, cryptographic session nonces with instant Redis revocation, and optional Google **gVisor** (`runsc`) user-space kernel virtualization.
* 🎨 **Refined Ash & Coral Design System**: Clean, modern UI with theme-synchronized light/dark modes, custom ash grey terminals, and subtle contrast controls.

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
                    ▼                                     ├─▶ PostgreSQL (:5434 / state)
        ┌───────────────────────┐                         ├─▶ Redis (:6380 / idle TTLs)
        │ Live App Sandboxes    │                         ├─▶ BuildKit Daemon (:1234)
        │ sandbox_<id>          │                         └─▶ Docker Daemon Socket
        │ Sidecars (Postgres/DB)│
        └───────────────────────┘
```

### Port Allocation Reference

| Service | Port | Host Binding | Purpose |
|---|---|---|---|
| **Frontend Console** | `3000` | `127.0.0.1` | Next.js 16 App Router UI & Monaco IDE |
| **API Control Plane** | `8080` | `127.0.0.1` | REST endpoints & live WebSocket telemetry |
| **Gateway Proxy** | `8090` | `127.0.0.1` | Reverse proxy with single-flight wake-on-request |
| **Edge Router (Traefik)** | `80`, `8081` | `0.0.0.0` | `*.sandbox.localhost` edge routing & Traefik dashboard (`:8081`) |
| **PostgreSQL** | `5434` | `127.0.0.1` | Primary persistent state & audit logs (`user:pass`, db `sandbox_platform`) |
| **Redis** | `6380` | `127.0.0.1` | Inactivity TTL markers (`lr:<id>`) & live metric caches |
| **BuildKit Daemon** | `1234` | `127.0.0.1` | TCP build daemon for Railpack image builds |

---

## Quick Start (Local Development)

### 1. Prerequisites
- **Docker Engine** & **Docker Compose v2** installed and running.
- **Go 1.25+** (tested against Go 1.25 / 1.26).
- **Node.js 20+** (LTS or Node 24).
- **GitHub OAuth App** with callback URL `http://localhost:8080/api/auth/callback/github`.
- **Railpack CLI** (optional for host builds; runs via BuildKit):
  ```bash
  curl -sSL https://railpack.com/install.sh | sh
  ```

### 2. Infrastructure Setup
```bash
# Clone the repository
git clone https://github.com/AyushCN/frontendformajor.git
cd frontendformajor
git checkout frontend

# Configure environment variables
cp .env.example .env
# Edit .env — set GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, ENCRYPTION_KEY, etc.

# Spin up Postgres (5434), Redis (6380), Traefik (80/8081), and BuildKit (1234)
docker compose -f docker-compose.dev.yml up -d
```

### 3. Run Database Migrations
```bash
cd backend
go run ./cmd/migrate up
```

### 4. Backend Service
```bash
cd backend
go run ./cmd/server
```

> **Development** (`APP_ENVIRONMENT=development`): OAuth configuration is optional; DevAuth is permitted.  
> **Production** (`APP_ENVIRONMENT=production`): `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, and `GITHUB_OAUTH_CALLBACK_URL` are **required** at startup. `DEV_AUTH` is forcibly disabled.

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
├── backend/                  # Go service (Chi, pgx, Docker Engine SDK, BuildKit)
│   ├── cmd/server/           # Application entrypoint with startup validation
│   ├── cmd/migrate/          # Database migration runner
│   ├── internal/api/         # REST routes, WebSockets, auth, CoW sandboxing
│   ├── internal/orchestrator/# Container lifecycle, builders, orphan reconciliation
│   ├── internal/reaper/      # Idle & expiry background workers with retry-on-failure
│   ├── internal/db/          # Typed database queries
│   └── migrations/           # Versioned SQL migrations (0001–0007)
├── frontend/                 # Next.js 16 (React 19, Tailwind CSS v4, Monaco Editor)
│   ├── app/                  # App Router pages (Landing, Dashboard, Projects, IDE)
│   ├── components/           # UI components, terminal consoles, theme toggle
│   └── lib/                  # Typed API client, auth state, theme provider
├── docs/                     # Detailed architectural specifications & guides
│   ├── ARCHITECTURE.md       # Routing, wake-on-request & builder pipeline
│   ├── SETUP.md              # Development environment runbook & troubleshooting
│   ├── API.md                # REST & WebSocket endpoint catalog
│   ├── DATABASE.md           # Schema definitions and data lifecycle models
│   ├── SECURITY.md           # Threat models, gVisor containment & session security
│   ├── RUNBOOK.md            # Production deployment and operations guide
│   └── SINGLE_TENANT_AUDIT.md # Engineering audit & remediation log
├── traefik/                  # Traefik dynamic file configuration
├── docker-compose.dev.yml    # Development infrastructure stack
└── docker-compose.prod.yml   # Production infrastructure blueprint
```

---

## Security Design

| Property | Implementation |
|---|---|
| **Authentication** | GitHub OAuth 2.0 with CSRF state validation |
| **Sessions** | `<githubID>:<issuedAt>:<nonce>` cookie — 30-day max TTL, instant Redis revocation on logout |
| **Secrets** | AES-256-GCM encryption of all stored tokens via `secret.Box` |
| **Container isolation** | `CapDrop: ALL`, `CapAdd: NET_BIND_SERVICE`, `no-new-privileges`, per-sandbox bridge networks |
| **Editor CoW** | An `EDITOR` modifying a shared sandbox gets an independent fork — the original is never touched |
| **Orphan recovery** | On startup, containers whose DB rows are expired/deleted are stopped and removed |
| **Teardown integrity** | A sandbox is never marked expired in the database until Docker teardown succeeds |
| **Production enforcement** | Missing OAuth config fails startup; DevAuth hardcoded `false` in production |

---

## CI Pipeline

The [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on `main`, `master`, `production`, and **`frontend`** branches:

| Job | What it checks |
|---|---|
| **Backend Unit Tests & Vetting** | `go vet`, `go test -v -count=1 ./...`, binary build |
| **End-to-End Smoke Test** | Nixpacks + Railpack real Docker deploy of `node-js-sample` |
| **Frontend TypeScript & Build** | `tsc --noEmit`, `next build` |

---

## Documentation Index

- 🛠️ **[docs/SETUP.md](docs/SETUP.md)** — Step-by-step development setup, port mapping, and debugging guide.
- 🏛️ **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — System architecture, gateway proxy, wake-on-request, and container lifecycles.
- ⚡ **[docs/LIFECYCLE.md](docs/LIFECYCLE.md)** — Lifecycle state machine, wake/expiry race fixes, grace periods, and data retention.
- 👥 **[docs/COLLABORATION.md](docs/COLLABORATION.md)** — Project collaboration, RBAC matrix, and Copy-on-Write isolation.
- 📡 **[docs/API.md](docs/API.md)** — Complete REST & WebSocket endpoint specifications.
- 🗄️ **[docs/DATABASE.md](docs/DATABASE.md)** — PostgreSQL schema, migrations, and data lifecycle.
- 🔒 **[docs/SECURITY.md](docs/SECURITY.md)** — Threat model, capability dropping, session revocation, and gVisor sandboxing.
- 🚀 **[docs/RUNBOOK.md](docs/RUNBOOK.md)** — Production operations, systemd units, automated recovery, and TLS.
- 🔍 **[docs/SINGLE_TENANT_AUDIT.md](docs/SINGLE_TENANT_AUDIT.md)** — Engineering audit findings and production readiness remediation log.

---

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.
