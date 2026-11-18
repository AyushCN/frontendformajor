# Architecture & System Design

This document details the architectural principles, container orchestration pipelines, runtime version provisioning, persistent build caching, networking models, and data flows powering **API Sandbox Links**.

---

## 1. System Overview & Component Split

The platform is designed around a strict decoupling between the **control plane** (management API and UI) and the **data plane** (sandbox network traffic, BuildKit builders, and dynamic container execution):

```
Browser ── :80 (Traefik Edge)      ──▶ Gateway :8090 (Wake Proxy)     ──▶ App Sandbox Container (:internal_port)
Browser ── :3000 (Next.js Console) ──▶ API :8080 (REST + WebSockets)  ──▶ PostgreSQL (:5434) & Redis (:6380)
                                                                       ├─▶ BuildKit Daemon (:1234)
                                                                       ├─▶ RuntimeAvailabilityManager
                                                                       │     ├─ Singleflight Deduplication
                                                                       │     └─ SharedRuntimeCache (LRU + Leases)
                                                                       └─▶ Docker Daemon Socket
```

### Core Services

1. **`backend/` (Go 1.25+ Binary)**: Runs two independent HTTP/TCP listeners:
   - **Port `:8080` (Control Plane)**: REST API and WebSocket streams for log tailing, container metrics, file editor operations, Git push, and deployment orchestration.
   - **Port `:8090` (Gateway Proxy)**: Low-latency reverse proxy that inspects incoming subdomain host headers, wakes hibernated containers on demand, and forwards traffic.
2. **`frontend/` (Next.js 16 App Router & React 19)**:
   - Developer console and in-browser IDE with Monaco Editor, Ash Grey terminal consoles, and zero-drift Git management.
   - Next.js BFF (Backend-For-Frontend) route handlers for Workspace Project scoping and collaborator roles.
3. **`traefik/dynamic.yml` (Edge Proxy)**:
   - Listens on ports `80` (HTTP) and `443` (TLS wildcard `*.sandbox.localhost`).
   - Uses file-provider routing to direct sandbox traffic directly to host gateway `:8090`.
4. **Development Infrastructure (`docker-compose.dev.yml`)**:
   - **PostgreSQL 16**: Port `5434` (persists users, sandboxes, deployments, runtime telemetry, and encrypted credentials).
   - **Redis 7**: Port `6380` (ephemeral idle TTL keys `lr:<id>`, live metrics buffer, and transient coordination).
   - **BuildKit Daemon**: Port `1234` (rootless OCI builder for Railpack and Dockerfile builds).

---

## 2. Ingress & Wake-on-Request Gateway

### Why a Dedicated Gateway Instead of Docker Labels?
Direct Docker-label routing in edge proxies (e.g. Traefik Docker provider) has significant drawbacks:
- It requires exposing the host `/var/run/docker.sock` to the public proxy.
- Container stop/start events trigger noisy routing table re-computations and race conditions.
- It cannot intercept requests to stopped containers to implement zero-latency auto-waking.

In API Sandbox Links, Traefik uses a static wildcard file rule:
```yaml
http:
  routers:
    sandbox-wildcard:
      rule: "HostRegexp(`^[a-z0-9-]+\\.sandbox\\.localhost$`)"
      service: gateway-backend
  services:
    gateway-backend:
      loadBalancer:
        servers:
          - url: "http://host.docker.internal:8090"
```

### The Single-Flight Wake Mechanism

```
Inbound Request (*.sandbox.localhost)
           │
           ▼
    Gateway (:8090)
           │
           ├──▶ Sandbox RUNNING?
           │         │
           │         ├─ YES ──▶ Touch Redis `lr:<id>` TTL (900s) ──▶ Reverse Proxy to Container
           │         │
           │         └─ NO (HIBERNATED)
           │                 │
           │                 ▼
           │           Single-Flight Lock (coalesces duplicate bursts)
           │                 │
           │                 ├─▶ docker start sandbox_<subdomain>
           │                 ├─▶ WaitHealthy (poll internal_port TCP accept)
           │                 ├─▶ Update DB status -> `running`
           │                 └─▶ Forward Request
```

1. **Activity Heartbeat**: Every forwarded HTTP request touches Redis key `lr:<sandbox_id>` with TTL equal to `idle_timeout_seconds` (default 900s / 15 mins).
2. **Idle Reaping**: The background idle reaper periodically sweeps active sandboxes. If `lr:<sandbox_id>` has expired, the container is cleanly stopped and marked `hibernated`.
3. **Transparent Wake**: When traffic reaches a hibernated sandbox, the gateway:
   - Acquires a single-flight mutex for that sandbox (preventing boot storms if concurrent requests arrive).
   - Issues `docker start <container_id>`.
   - Polls the sandbox's `internal_port` until TCP handshakes succeed (`WaitHealthy`).
   - Flips status in Postgres to `running`.
   - Reverse-proxies the original HTTP request seamlessly.

---

## 3. Runtime Resolution, Availability & Shared BuildKit Cache

To provide fast, reproducible, and deterministic builds without installing language compilers or runtimes on the Go backend host, API Sandbox Links implements a dedicated runtime management subsystem.

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

### 3.1 8-Tier Runtime Precedence Hierarchy

The `RuntimeResolver` resolves runtime requirements against a strict 8-tier hierarchy, guaranteeing that explicit developer selections are honored while providing safe canonical defaults:

1. **Explicit User Selection**: User-provided `build_config.runtime_version` or API parameter.
2. **Repository Configuration**: Config files committed to the repository root (`railpack.json`, `nixpacks.toml`, `.tool-versions`).
3. **Package Manifests**: Version constraints embedded in dependency manifests (`package.json.engines`, `go.mod`, `pom.xml`, `build.gradle`, `requirements.txt`, `Pipfile`, `pyproject.toml`).
4. **Language Version Files**: Standard ecosystem version markers (`.python-version`, `.node-version`, `.nvmrc`, `.go-version`, `.java-version`).
5. **Dependency Compatibility Inference**: Analyzes pinned libraries with known runtime bounds (e.g., PyTorch / TensorFlow ML compatibility matrices).
6. **Builder Default Versions**: Built-in defaults specific to the selected builder.
7. **Platform Compatibility Policy**: Platform-wide baseline policies.
8. **Canonical Fallback Defaults**: Platform fallback defaults (Python 3.11.9, Node 22.14.0, Go 1.24.0, Java 21.0.2).

### 3.2 Canonical Version Canonicalization & Validation

Runtimes are mapped to exact patch versions to prevent build non-determinism:

| Language | Example Requests | Canonical Resolved Version |
|---|---|---|
| **Python** | `3`, `3.11`, `3.11.x`, `3.11.9` | `3.11.9` |
| **Node.js** | `22`, `22.x`, `22.14`, `22.14.0` | `22.14.0` |
| **Go** | `1.24`, `1.24.x`, `go1.24` | `1.24.0` |
| **Java** | `21`, `21.x`, `java21` | `21.0.2` |

Requested versions are validated against:
- **Host OS & CPU Architecture**: Verified via `ValidateArchOS(os, arch)` for `linux` and `darwin` on `amd64` (`x86_64`) and `arm64` (`aarch64`).
- **Builder Compatibility**: Checks whether Railpack natively supports the requested version.
- **Dependency Bounds**: Detects ML or framework incompatibilities (e.g., PyTorch 2.2 requiring Python 3.8–3.11).

The validation output is classified into 5 discrete `VersionStatus` categories:
- `VERSION_SUPPORTED`: Version is fully supported by the primary builder (Railpack) and available or provisionable.
- `VERSION_UNAVAILABLE`: Version is known to the policy but distribution binaries are temporarily unreachable.
- `VERSION_UNSUPPORTED`: Version is unsupported or end-of-life and rejected by policy without silent upgrade.
- `VERSION_INCOMPATIBLE`: Version conflicts with project dependencies or target OS/architecture.
- `VERSION_PROVISIONING_REQUIRED`: Version is valid and supported, but the runtime artifact must be acquired before build execution.

> [!IMPORTANT]
> The resolver never silently mutates or upgrades an unavailable requested version to a different major or minor version. If a requested version cannot be fulfilled, the build fails with an actionable error.

### 3.3 Runtime Availability Manager & Single-Flight Coordination

The `RuntimeAvailabilityManager` coordinates runtime acquisition and build execution:
- **Concurrency Deduplication**: When multiple concurrent builds request the same missing runtime, `singleflight.Group` ensures only one provisioning worker runs. All other requests await the result of the single flight.
- **Cancellation Resilience**: Provisioning is initiated via `singleflight.Group.DoChan`. If one requesting client cancels its HTTP request or context, the background provisioning operation continues to completion and registers the resulting artifact in the cache for other concurrent and future builds.
- **Exponential Failure Backoff**: If provisioning fails, the failure timestamp and count are recorded. Subsequent requests during the backoff window fail immediately without hammering BuildKit or external registries.
- **Active Build Lease Management**: When a build begins using a cached runtime artifact, it acquires an active lease (`AcquireLease`). The lease is released (`ReleaseLease`) upon build completion. Leased artifacts are protected from cache eviction.

### 3.4 Shared Runtime Cache vs. Package-Manager Caches

The platform strictly decouples **runtime cache** (the language interpreter, compiler, and standard library) from **package-manager caches** (downloaded module and dependency archives):

#### Shared Runtime Cache (`SharedRuntimeCache`)
- **6-Factor Deterministic Identity**:
  $$\text{Key} = \text{SHA256}(\text{Language} : \text{Version} : \text{OS} : \text{Arch} : \text{DistributionID} : \text{BuilderVersion})$$
- Stored as immutable OCI layer artifacts in BuildKit.
- Managed by an in-memory registry with byte-size quotas (default 20GB) and high-watermark LRU eviction (85% capacity threshold).
- **Eviction Safety**: `EvictLRU` skips any artifact where `ActiveBuilds > 0`, ensuring active builds are never corrupted.

#### Package-Manager Cache Mounts (`CacheMountConfig`)
BuildKit `--mount=type=cache` mounts persist dependency downloads across builds without bloating container image layers:

| Package Manager | Cache Path | BuildKit Sharing Mode | Isolation Scope |
|---|---|---|---|
| **pip** | `/root/.cache/pip` | `shared` | `asl-pip-<os>-<arch>` (or tenant-scoped) |
| **uv** | `/root/.cache/uv` | `shared` | `asl-uv-<os>-<arch>` |
| **npm** | `/root/.npm` | `shared` | `asl-npm-<os>-<arch>` |
| **pnpm** | `/root/.local/share/pnpm/store` | `shared` | `asl-pnpm-<os>-<arch>` |
| **yarn** | `/usr/local/share/.cache/yarn` | `shared` | `asl-yarn-<os>-<arch>` |
| **go build** | `/root/.cache/go-build` | `shared` | `asl-go-build-<os>-<arch>` |
| **go modules** | `/go/pkg/mod` | `shared` | `asl-go-mod-<os>-<arch>` |
| **maven** | `/root/.m2` | `shared` | `asl-maven-<os>-<arch>` |
| **gradle** | `/root/.gradle` | `shared` | `asl-gradle-<os>-<arch>` |
| **cargo** | `/usr/local/cargo/registry` | `shared` | `asl-cargo-<os>-<arch>` |
| **ccache** | `/root/.ccache` | `shared` | `asl-ccache-<os>-<arch>` |

- **Tenant Isolation**: When `PackageCacheOptions.TenantID` is specified, the cache mount ID is scoped with the tenant ID (e.g. `asl-pip-tenant123-linux-amd64`), preventing untrusted builds from reading or corrupting private cached packages of other tenants.
- **Locking Semantics**: High-concurrency operations use `sharing=locked` or `sharing=private` when simultaneous writes could cause package index corruption.

### 3.5 Legacy Runtime Strategy (Python 3.5 Handling)

Railpack's official Python provider supports Python 3.10 and newer. To support legacy projects without compromising platform reliability:

1. **Detection**: `LegacyRuntimePolicy.IsLegacy()` identifies runtimes outside Railpack's native envelope:
   - Python: 3.5, 3.6, 3.7, 3.8, 3.9
   - Node.js: 14, 16
   - Java: 11
2. **Strategy Routing**: `StrategySelector.SelectBuilderWithRuntime` automatically routes these builds to `dockerfile` with selection source `LEGACY_RUNTIME_STRATEGY`.
3. **Context Staging**: An isolated temporary directory is created via `ContextStaging`. An official, verified base image Dockerfile (e.g. `python:3.5-slim`) is generated containing BuildKit cache mounts. The user's Git repository is left completely unmodified.
4. **Obsolete/EOL Rejection**: Truly obsolete or end-of-life runtimes (Python 3.4, Python 2.7, Node 10, Go 1.15, Java 8) are identified by `IsObsolete()` and rejected with an explicit, actionable error explaining the security risk and recommending an upgrade. Runtimes are never compiled on the host and unverified binaries are never downloaded.
5. **Security Gate**: Legacy runtime support can be globally disabled by setting `policy.SetAllowLegacy(false)`.

---

## 4. Builder Engine & Precedence Resolution

The platform supports pluggable builders with deterministic precedence:

```
                      Repository Deployment Request
                                    │
                                    ▼
                     Explicit User Config Provided?
                                    │
                     ├── YES ───────┴──────── NO ──┐
                     ▼                             ▼
            Run Explicit Builder          Dockerfile in Repo Root?
         (Dockerfile/Railpack/Nixpacks)            │
                                           ├── YES ┴──────── NO ──┐
                                           ▼                      ▼
                                   Dockerfile Builder      Legacy Runtime Required?
                                  (Native BuildKit OCI)   (Python 3.5-3.9, Node 14/16)
                                                                  │
                                                          ├── YES ┴──────── NO ──┐
                                                          ▼                      ▼
                                                  Legacy Dockerfile       Railpack Builder
                                                  (Isolated Staging)     (tcp://127.0.0.1:1234)
```

1. **Explicit Builder Selection**: If `.env` or deployment payload specifies `BUILDER=...`, that choice takes precedence.
2. **Native Dockerfile Precedence**: If the repository root contains a `Dockerfile`, the orchestrator builds it directly using BuildKit.
3. **Legacy Runtime Isolation**: If the project requires a legacy runtime (e.g. Python 3.5), it is routed to an isolated Dockerfile build.
4. **Railpack (Default Cloud-Native Builder)**:
   - Modern, high-performance builder using Railpack buildpacks.
   - Compiles OCI image layers via BuildKit daemon on `tcp://127.0.0.1:1234`.
   - Reuses cached layers and package mounts across builds.
5. **Nixpacks (Supported Deprecated Fallback)**:
   - Available via `BUILDER=nixpacks` during migration. Emits structured deprecation notices.

### Automatic Port Discovery (`DiscoverPort`)
Application frameworks often ignore the assigned `PORT` environment variable and bind their own hardcoded default (e.g. Next.js on `3000`, Flask on `5000`, Go on `8080`, Rails on `3000`).

To ensure zero false-positive deployment failures:
1. `DiscoverPort` inspects the planned port first.
2. If the planned port does not accept TCP within initial grace periods, it scans candidate ports:
   `[8080, 8000, 3000, 5000, 5001, 4000, 9000, 8888, 80]`
3. Whichever port responds to TCP health checks is dynamically persisted as `internal_port` and used by the gateway.

---

## 5. Sidecar Databases & Zero-Leak Isolation

When an application requires a database, the platform provisions a dedicated, lightweight sidecar container:

```
┌────────────────────────────────────────────────────────┐
│ Bridge Network: sandbox_<id[:8]>                        │
│                                                        │
│  ┌───────────────────────┐    ┌──────────────────────┐ │
│  │ Sandbox App Container │    │ Sidecar Database DB  │ │
│  │ Name: sandbox_<subdomain>│ ──▶│ Host: db_<id[:8]>    │ │
│  │ Port: internal_port   │    │ Port: 5432 or 3306   │ │
│  └───────────────────────┘    └──────────────────────┘ │
└────────────────────────────────────────────────────────┘
```

- **Automatic Dependency Detection**: `DetectDatabaseNeed` scans repository manifests (`package.json`, `go.mod`, `requirements.txt`, `Gemfile`, `schema.prisma`) for drivers (`pg`, `mysql2`, `psycopg2`, Prisma, GORM, Sequelize).
- **Network Isolation**: The sidecar runs on the sandbox's dedicated bridge network (`sandbox_<id[:8]>`). It has **no host port published** and is completely unreachable from other sandboxes or external traffic.
- **Environment Injections (`dbEnvFromURL`)**: Injects all standard database variable variants so apps connect out-of-the-box:
  - `DATABASE_URL`
  - `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`
  - `MYSQL_HOST`, `MYSQL_PORT`, `MYSQL_USER`, `MYSQL_PASSWORD`, `MYSQL_DATABASE`

---

## 6. Workspace Projects & Multi-Tenancy Scoping

Engineering teams organize disposable sandboxes into collaborative **Workspace Projects**:

```
                       Workspace Projects Store
                   (/tmp/api-sandbox-links/projects.json)
                                     │
           ┌─────────────────────────┴─────────────────────────┐
           ▼                                                   ▼
     Default Workspace                               Custom Workspace Project
   (id: "default-project")                             (id: "proj-microservices")
           │                                                   │
  Holds unassigned sandboxes                          Holds explicitly linked sandboxes
  and legacy environments                             via `sandboxIds: [...]`
```

### Scoping Rules (`filterSandboxesForProject`)
- Every sandbox created outside a specific workspace project belongs to the `default-project` ("Default Workspace").
- When viewing a custom workspace project, the BFF route filters environments strictly: only sandboxes whose IDs exist in `project.sandboxIds` are returned.
- Repositories and workspaces are isolated per-project, preventing cross-tenant leakage in multi-project setups.
- **Collaborator RBAC**:
  - `OWNER`: Full administrative rights (invite members, delete project, transfer ownership).
  - `EDITOR`: Deploy sandboxes, trigger redeployments, edit files in IDE, push Git commits.
  - `VIEWER`: Read-only access to preview URLs, build logs, and application metrics.

---

## 7. Live Log & Telemetry Pipeline

### Dual-Pipe WebSocket Logging
Docker interleaves `stdout` and `stderr` behind an 8-byte multiplex header when TTY is disabled. The backend uses `stdcopy.StdCopy` to maintain two independent streams:

| Stream | Backfill Policy | Rationale |
|---|---|---|
| **`stdout`** | Last 200 lines | High volume; replaying the entire history would delay connection without providing debugging value. |
| **`stderr`** | Entire history (`--tail all`) | Low volume; critical diagnostic errors emitted at container start must never be clobbered by high-volume stdout logs. |

### Real-Time Metrics Pipeline
```
Container (docker stats) ──▶ StatsCollector (1s tick)
                                 ├─▶ Live WebSocket Stream (:8080/stats/ws) ──▶ In-Browser Chart
                                 └─▶ Redis Buffer `stats:latest:<id>` (10s TTL)
                                          │
                                 Every 10s (StatsFlushWorker)
                                          ▼
                                 Postgres `resource_snapshots`
                                          │
                                 Pruned after 7 days (SnapshotReaper)
```

1. **Normalized CPU Utilization**: Docker's `cpu_usage.total_usage` is a monotonic counter. The backend computes rates between successive ticks (`computeCPUPercent`), ensuring accurate utilization percentages.
2. **True Memory Usage**: Reclaimable page cache (`inactive_file`) is subtracted from raw memory usage to reflect true application memory pressure.

---

## 8. Container Hardening & Google gVisor

Every sandbox container is deployed with hardened Linux containment:

| Control | Flag / Setting | Purpose |
|---|---|---|
| **Capability Dropping** | `CapDrop: ["ALL"]` | Strips all Linux capabilities by default. |
| **Privilege Escalation** | `SecurityOpt: ["no-new-privileges:true"]` | Prevents setuid binaries inside the container from acquiring elevated privileges. |
| **Process Supervision** | `Init: true` (tini) | Reaps zombie processes and handles POSIX signal forwarding. |
| **Google gVisor (Opt-in)** | `SANDBOX_RUNTIME="runsc"` | Replaces standard `runc` with gVisor's user-space kernel (`runsc`), virtualizing all syscalls and preventing container breakouts. |
| **Read-Only Rootfs** | `SANDBOX_HARDENED=true` | Mounts root filesystem read-only with a 64MB ephemeral `tmpfs` at `/tmp`. |
