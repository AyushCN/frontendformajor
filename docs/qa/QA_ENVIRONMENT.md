# API Sandbox Links — Isolated QA Environment Specification

> **Document Version:** 1.0.0  
> **Purpose:** Repeatable, isolated testing infrastructure preventing collision with production or development data.  

---

## 1. Environment Architecture & Isolation Boundaries

To guarantee that automated test suites, chaos injection, and load testing never corrupt local developer sandboxes or production data, all QA runs must operate in a dedicated namespace with explicit resource labeling:

```
                      [ Host System Engine ]
                                │
               ┌────────────────┴────────────────┐
               ▼                                 ▼
      [ Development Stack ]             [ QA Isolated Stack ]
     - DB: `sandbox_dev`               - DB: `sandbox_qa` (Port 5433)
     - Redis: `redis://:6379/0`        - Redis: `redis://:6380/1`
     - Network: `dev_default`          - Network: `qa_isolated_net`
     - Label: `env=dev`                - Label: `qa.test=true`
```

---

## 2. Docker Compose QA Specification (`docker-compose.qa.yml`)

Save the following configuration as `docker-compose.qa.yml` in the project root:

```yaml
version: "3.8"

networks:
  qa_net:
    name: api_sandbox_qa_network
    driver: bridge
    labels:
      qa.test: "true"

volumes:
  qa_postgres_data:
    name: api_sandbox_qa_pgdata
  qa_redis_data:
    name: api_sandbox_qa_redisdata

services:
  qa_postgres:
    image: postgres:16-alpine
    container_name: api_sandbox_qa_postgres
    environment:
      POSTGRES_USER: qa_user
      POSTGRES_PASSWORD: qa_secure_password_2026
      POSTGRES_DB: api_sandbox_qa
    ports:
      - "5433:5432"
    volumes:
      - qa_postgres_data:/var/lib/postgresql/data
    networks:
      - qa_net
    labels:
      qa.test: "true"
      qa.component: "postgres"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U qa_user -d api_sandbox_qa"]
      interval: 3s
      timeout: 3s
      retries: 10

  qa_redis:
    image: redis:7-alpine
    container_name: api_sandbox_qa_redis
    ports:
      - "6380:6379"
    volumes:
      - qa_redis_data:/data
    networks:
      - qa_net
    labels:
      qa.test: "true"
      qa.component: "redis"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 3s
      timeout: 3s
      retries: 10

  qa_traefik:
    image: traefik:v3.0
    container_name: api_sandbox_qa_traefik
    command:
      - "--api.insecure=true"
      - "--providers.docker=true"
      - "--providers.docker.exposedbydefault=false"
      - "--entrypoints.web.address=:80"
    ports:
      - "8091:80"
      - "8092:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks:
      - qa_net
    labels:
      qa.test: "true"
      qa.component: "traefik"
```

---

## 3. QA Configuration File (`.env.qa`)

```env
# Server & Port Overrides
PORT=8081
GATEWAY_PORT=8091
FRONTEND_URL=http://localhost:3001

# Isolated Database & Cache
DATABASE_URL=postgres://qa_user:qa_secure_password_2026@localhost:5433/api_sandbox_qa?sslmode=disable
REDIS_URL=redis://localhost:6380/1

# Master Secret Key for AES-256-GCM (32 hex bytes)
SECRET_BOX_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

# Docker Sandboxing & Scoping
DOCKER_NETWORK=api_sandbox_qa_network
SANDBOX_LABEL_FILTER=qa.test=true
SANDBOX_WORKSPACE_DIR=/tmp/api_sandbox_qa_workspaces

# Fast Timers for Deterministic QA
IDLE_TIMEOUT_SECONDS=30
HIBERNATED_EXPIRATION_SECONDS=60
WORKSPACE_RETENTION_SECONDS=120
REAPER_INTERVAL=5s

# Testing Auth Mode (Bypasses GitHub External OAuth during CI)
DEV_AUTH=true
```

---

## 4. Lifecycle & Safe Teardown Procedures

### 4.1 Launching the QA Environment
```bash
# 1. Start isolated infrastructure
docker compose -f docker-compose.qa.yml up -d --wait

# 2. Run database migrations on isolated QA DB
cd backend && go run ./cmd/migrate --env=qa

# 3. Launch backend pointing to QA config
GOPATH=~/.gopath go run ./cmd/server --config=../.env.qa
```

### 4.2 Safe Cleanup & Purge Procedure
The cleanup script **strictly selects containers and volumes matching the label `qa.test=true`**. It will never delete or touch development or production containers.

```bash
#!/usr/bin/env bash
set -euo pipefail

echo "==> Cleaning isolated QA containers..."
docker ps -aq --filter "label=qa.test=true" | xargs -r docker rm -f

echo "==> Cleaning isolated QA networks..."
docker network prune -f --filter "label=qa.test=true"

echo "==> Wiping temporary QA workspaces..."
rm -rf /tmp/api_sandbox_qa_workspaces

echo "==> Stopping QA compose infrastructure..."
docker compose -f docker-compose.qa.yml down -v

echo "==> QA cleanup completed safely without touching host or dev resources."
```
