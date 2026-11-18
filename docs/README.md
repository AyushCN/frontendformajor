# API Sandbox Links — Documentation Index

Turn any public GitHub repository into a disposable, time-limited, remotely-accessed cloud sandbox. This is the master index and guide to all project documentation.

---

## Documentation Catalog

| Document | Purpose & Scope | Key Code References |
|---|---|---|
| **[SETUP.md](SETUP.md)** | Step-by-step local development setup, environment variables, build orders, migration runner, port configurations, and troubleshooting. | [`backend/internal/config/config.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/config/config.go), [`docker-compose.dev.yml`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/docker-compose.dev.yml), [`.env.example`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/.env.example) |
| **[ARCHITECTURE.md](ARCHITECTURE.md)** | System design, two-listener split (`:8080` API vs `:8090` Gateway), single-flight wake-on-request, runtime resolution, availability management, BuildKit shared cache, and legacy runtimes. | [`backend/internal/orchestrator/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/orchestrator/), [`backend/internal/proxy/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/proxy/), [`traefik/dynamic.yml`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/traefik/dynamic.yml) |
| **[LIFECYCLE.md](LIFECYCLE.md)** | Sandbox lifecycle state machine, multi-user activity tracking, 60s grace periods, active operation protection, and data retention. | [`backend/internal/reaper/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/reaper/), [`backend/internal/cache/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/cache/) |
| **[COLLABORATION.md](COLLABORATION.md)** | Project collaboration, RBAC matrix (`OWNER`, `EDITOR`, `VIEWER`), invitations, and Default Personal Space vs Shareable Projects. | [`frontend/lib/projects-store.ts`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/lib/projects-store.ts), [`frontend/app/api/projects/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/app/api/projects/) |
| **[API.md](API.md)** | Complete REST & WebSocket API reference: Sandboxes, Workspace Projects, Collaborators, Monaco Editor file operations, and Git push routes. | [`backend/internal/api/router.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/router.go), [`frontend/app/api/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/frontend/app/api/) |
| **[DATABASE.md](DATABASE.md)** | PostgreSQL schema, migrations (0001–0008), GORM entity models, runtime telemetry persistence, and snapshot retention. | [`backend/migrations/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/migrations/), [`backend/internal/db/models.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/db/models.go) |
| **[SECURITY.md](SECURITY.md)** | Security posture, Linux capability dropping (`CapDrop: ALL`), Google gVisor kernel containment (`runsc`), cache isolation, AES-256 token encryption, and threat models. | [`backend/internal/orchestrator/lifecycle.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/orchestrator/lifecycle.go), [`backend/internal/api/auth.go`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/backend/internal/api/auth.go) |
| **[RUNBOOK.md](RUNBOOK.md)** | Production operations, single-node deployment runbook, automated recovery, systemd unit configurations, and TLS management. | [`docker-compose.prod.yml`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/docker-compose.prod.yml), [`traefik/`](file:///home/swordrookie/projects/api_sandbox_links/api-sandbox-links/traefik/) |
| **[PRD.md](PRD.md)** | Product requirements document, feature specifications, performance milestones, and architectural acceptance criteria. | Specifications & feature deliverables |

---

## Architecture Flow Overview

```
Browser ── :3000 (Next.js Console) ──▶ API :8080 (REST + WebSockets)  ──▶ PostgreSQL (:5434) & Redis (:6380)
Browser ── :80 (Traefik Edge)      ──▶ Gateway :8090 (Wake Proxy)     ──▶ App Sandbox Container (:internal_port)
```

1. **Control Plane (`:8080`)**: Handled by the Go `chi` router. Manages sandbox lifecycle, workspace project scoping, user authentication, runtime availability resolution, Monaco IDE file trees, and live metric WebSocket streams.
2. **Gateway Proxy (`:8090`)**: Intercepts inbound traffic for all `*.sandbox.localhost` subdomains. If a target container is hibernated, it wakes the container, awaits TCP healthiness, and reverse-proxies the request transparently.
3. **Builder Engine & Shared Cache (`:1234`)**: Powered by a rootless BuildKit daemon communicating with Railpack and the `RuntimeAvailabilityManager`. Uses 6-factor deterministic runtime artifact caching and package-manager cache mounts (`pip`, `uv`, `npm`, `pnpm`, `yarn`, `go`, `maven`, `gradle`).

---

## Verifying Stack Health

Before troubleshooting UI symptoms, verify that the backend and edge proxy are listening:

```bash
# Check control plane health
curl -s http://localhost:8080/api/health
# Response: {"status":"ok","time":"...","dev_auth":true}

# Check edge Traefik router
curl -I http://localhost:80
```

A browser reporting WebSocket close code `1006` typically indicates the backend process is stopped or inaccessible rather than a protocol failure. Refer to [SETUP.md](SETUP.md) for diagnostics.
