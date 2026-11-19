# API Sandbox Links — Performance, Concurrency & Scalability Test Plan

> **Document Version:** 1.0.0  
> **Testing Scope:** Throughput, Latency, Concurrency Limits, Gateway Overhead & Resource Saturation  

---

## 1. Performance Objectives & Service Level Indicators (SLIs)

| Metric | Target SLA / SLI | Degradation Threshold | Failure Threshold |
| :--- | :--- | :--- | :--- |
| **API Response Time (CRUD)** | P95 < 80ms | P95 > 250ms | P95 > 1,000ms |
| **Sandbox Listing (Batch Association)** | P95 < 45ms (0 N+1 Queries) | P95 > 150ms | P95 > 500ms |
| **Gateway Throughput (Running)** | > 2,500 RPS | < 1,000 RPS | < 250 RPS |
| **Wake-on-Request (Cold Startup)**| P95 < 4.5s | P95 > 10.0s | P95 > 30.0s |
| **Concurrent Build Compilations** | 4 parallel builds sustained | 2 parallel builds | 1 build starves host |
| **Monaco File Save Latency** | P95 < 60ms | P95 > 200ms | P95 > 800ms |
| **WebSocket Log Stream Delivery** | Latency < 100ms | Latency > 500ms | Backpressure drops |

---

## 2. Load & Concurrency Test Scenarios

### PERF-01: Thundering Herd on Wake-on-Request
- **Objective:** Verify that 100 simultaneous HTTP requests hitting a hibernated sandbox share a single wake call via `wakingMu` and resolve cleanly without container port collisions or 502 errors.
- **Tooling:** k6 script.
- **Profile:**
  - Virtual Users (VUs): 100
  - Duration: 15s spike
  - Target: `http://weather-api-1db5cc.sandbox.localhost:8090/api/data`
- **Expected Outcome:**
  - `docker start` is executed exactly 1 time.
  - Zero 502 / 503 / 504 errors returned.
  - 100% of requests succeed once container readiness check passes.

### PERF-02: Elimination of N+1 Queries in Sandbox Listing
- **Objective:** Verify `ListSandboxes` and `handleListSandboxes` perform batch project ID lookups in a single SQL query (`GetProjectIDsForSandboxes`) rather than issuing 1 query per sandbox.
- **Execution:**
  1. Populate test tenant with 50 sandboxes distributed across 5 projects.
  2. Execute `GET /api/sandboxes`.
  3. Monitor PostgreSQL query log via `pg_stat_statements` or `log_statement = 'all'`.
- **Expected Outcome:**
  - Maximum 2 SQL queries executed (1 for sandboxes, 1 for batch project mapping).
  - P95 response time < 50ms.

### PERF-03: Gateway Reverse Proxy Latency Overhead
- **Objective:** Measure latency overhead introduced by the Go reverse proxy (`wake_middleware` and `ReverseProxy`) compared to direct container access.
- **Test:**
  - Direct container benchmark: `http://172.18.0.5:3000/ping`
  - Gateway proxy benchmark: `http://app-123456.sandbox.localhost:8090/ping`
- **Expected Outcome:** Gateway introduces < 2.5ms median latency overhead per request.

### PERF-04: Sustained WebSocket Log Stream Saturation
- **Objective:** Test backend and browser stability when a container outputs continuous heavy logs (1,000 lines/second).
- **Execution:**
  1. Run container script: `while true; do echo "[$(date)] Telemetry stress test payload log entry with high entropy data"; done`.
  2. Connect 5 concurrent WebSocket clients to `/api/sandboxes/{id}/logs/ws`.
  3. Monitor backend CPU/memory and client browser heap.
- **Expected Outcome:**
  - Backend goroutines remain bounded; memory buffers do not leak.
  - Client WebSocket connections remain open without frame drops.

---

## 3. k6 Test Script Example: Gateway Concurrency

```javascript
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '5s', target: 50 },
    { duration: '10s', target: 100 },
    { duration: '5s', target: 0 },
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<3000'],
  },
};

export default function () {
  const res = http.get('http://weather-api-1db5cc.sandbox.localhost:8090/health', {
    headers: { 'Host': 'weather-api-1db5cc.sandbox.localhost:8090' },
  });
  check(res, {
    'is status 200': (r) => r.status === 200,
  });
  sleep(0.1);
}
```
