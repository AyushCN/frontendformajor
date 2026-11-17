# Sandbox Lifecycle Management & Security Specification

This document details the complete sandbox lifecycle state machine, idle detection, multi-user activity tracking, hibernation grace periods, bounded operation protection, and data retention rules implemented in **API Sandbox Links**.

---

## 1. Lifecycle State Machine

```
                   ┌────────────┐
                   │   QUEUED   │
                   └─────┬──────┘
                         │ (Worker picks up build)
                         ▼
                   ┌────────────┐
                   │  BUILDING  │
                   └─────┬──────┘
                         │ (Build succeeds, container starts)
                         ▼
                   ┌────────────┐  idle timeout expires
       ┌──────────▶│  RUNNING   │──────────────────────┐
       │           └─────▲──────┘                      │
       │                 │                             ▼
       │ (HTTP wake-up / │                     ┌───────────────┐
       │  singleflight)  │                     │ IDLE WARNING  │ (within 2m of timeout)
       │                 │                     └───────┬───────┘
       │           ┌─────┴──────┐                      │
       │           │   WAKING   │                      ▼
       │           └─────▲──────┘              ┌───────────────┐
       │                 │                     │ GRACE PERIOD  │ (60s countdown)
       │                 │                     └───────┬───────┘
       │           ┌─────┴──────┐                      │ (no member reset)
       └───────────│ HIBERNATED │◀─────────────────────┘
                   └─────┬──────┘ (Container stopped, memory freed, disk preserved)
                         │
                         │ (Hard lifetime ExpiresAt reached from ANY state)
                         ▼
                   ┌────────────┐
                   │  EXPIRED   │ (Runtime container destroyed; workspace data retained for 7 days)
                   └─────┬──────┘
                         │
                         ├────────────────────────────────────────┐
                         │ (After 7 days retention)               │ (User triggers "Restore to New Sandbox")
                         ▼                                        ▼
                   ┌────────────┐                           ┌────────────┐
                   │  DELETED   │                           │ NEW QUEUED │ (Fresh UUID, cloned files)
                   └────────────┘                           └────────────┘
```

### Irreversible Expiration Rule
Per platform security policy, **an expired container runtime is strictly irreversible and must never be revived** (`docker start` on an expired container is forbidden).
Restoration is supported exclusively via `POST /api/sandboxes/{id}/restore`, which spawns a **brand new sandbox instance** with a fresh UUID and copies the preserved workspace files into the new sandbox directory.

---

## 2. Multi-User Activity Tracking

In shared collaborative projects, multiple authorized developers may be interacting with the same sandbox simultaneously.

- **Activity Per Member**: Activity touches record per-user keys (`act:<sandboxID>:<userID>`) alongside the aggregate idle timer (`lr:<sandboxID>`).
- **One Active Member Protects All**: A sandbox will **never** hibernate while any authorized project member is actively editing files, typing in the terminal, or sending authenticated requests.
- **Server-Side Validation**: Idle timestamps are strictly server-controlled using UTC epoch timestamps. Client-provided timestamps are ignored.
- **Synthetic Traffic Filtering**: The proxy filters out synthetic probes (`/health`, `/ping`, `/favicon.ico`, `/__webpack`, `/_next`) and WebSocket status heartbeats so passive open tabs do not keep sandboxes alive indefinitely.

---

## 3. Hibernation Warning & Grace Period

1. **Idle Countdown Window**:
   When the remaining idle TTL is $\le 2\text{ minutes}$, the server sets a warning flag (`HasHibernationWarning`).
2. **Grace Period Activation**:
   When the idle TTL reaches $0$, the sandbox enters a **60-second grace period** (`InGracePeriod`).
3. **Hard Expiration Clamping**:
   The grace period can **never** bypass hard lifetime expiration (`ExpiresAt`). If `now + grace > ExpiresAt`, the grace duration is truncated to `time.Until(ExpiresAt)`.
4. **Member Activity Reset**:
   Any authorized user clicking **"I'm Still Here"** or **"Keep Alive"** triggers `POST /api/sandboxes/{id}/activity`, instantly clearing warning and grace states and restoring the full idle timer.

---

## 4. Active Operation Protection

Long-running operations (such as dependency installation or builds) should temporarily protect sandboxes from hibernation even if there are no inbound HTTP requests.

### Supported Operation Types
Only verified, platform-defined operation types are permitted:
- `BUILD`
- `DEPENDENCY_INSTALL`
- `GIT_PULL`
- `GIT_COMMIT`
- `UPLOAD`
- `MIGRATION`

*Arbitrary or unapproved string types are rejected with HTTP 400.*

### Bounded Protection
- **Upper Bound**: Operations are bounded to a maximum duration of **15 minutes** (configurable up to 30 minutes).
- **Abandoned Operation Recovery**: When checking active operations, if `time.Since(StartedAt) > MaxDurationSeconds`, the operation is automatically purged as abandoned and no longer blocks hibernation.

---

## 5. Gateway Wake-Up & Security Enforcement

When an inbound request arrives for a hibernated sandbox:
1. **Hard Expiration Check**: If `ExpiresAt` has passed or status is `EXPIRED`, the gateway immediately rejects with **HTTP 410 Gone**.
2. **Authorization & RBAC**: The user's session cookie is verified. If the user is not an authorized project member (or their membership was revoked), the gateway rejects with **HTTP 403 Forbidden**.
3. **Singleflight Deduplication**: If multiple concurrent requests arrive while the container is spinning up, they share a single execution routine via internal channels, preventing Docker socket thundering herds.
4. **Readiness Status**: Requests with `?readiness=1` or `Accept: application/json` receive `202 Accepted` (`{"status":"WAKING","ready":false}`) until the container port is healthy.

---

## 6. Workspace Data Preservation vs Runtime Deletion

To balance infrastructure cost with developer data safety, runtime destruction is completely decoupled from disk retention:

| Lifecycle Event | Container & Runtime | Network & Sidecars | Workspace Files |
|---|---|---|---|
| **Hibernation** | Stopped (`docker stop`) | Preserved | Preserved |
| **Expiration** | Destroyed (`docker rm -f`) | Removed | **Retained in cold storage for 7 days** |
| **Retention Deadline** | N/A | N/A | Permanently deleted from disk |
| **Restoration** | Fresh container created | Fresh network | Cloned into new sandbox directory |

---

## 7. Verification Test Suite (Section H)

All 12 required failure-injection and unit test cases are implemented and passing in `backend/internal/proxy/lifecycle_extended_test.go`:

| Test Name | Behavior Verified |
|---|---|
| `TestMultiUserActivity_OneActiveOneIdle` | One active collaborator keeps the environment alive |
| `TestMultiUserActivity_AllIdle` | All idle collaborators trigger warning and grace countdown |
| `TestActiveOperationProtection_Build` | Active build operation temporarily prevents hibernation |
| `TestAbandonedOperationRecovery` | Operations exceeding bounded duration are recovered |
| `TestFakeOperationRejected` | Malicious/arbitrary operation names are rejected |
| `TestWakeUp_ExpiredSandboxReturns410` | Expired sandbox requests return HTTP 410 Gone |
| `TestWakeUp_RevokedMemberReturns403` | Revoked project members receive HTTP 403 Forbidden |
| `TestWakeUp_ConcurrentDeduplication` | Singleflight collapses simultaneous wakeups to 1 container start |
| `TestPreviewSession_SyntheticTrafficIgnored` | Dev assets and health probes do not bump idle timers |
| `TestWebSocketHeartbeatDoesNotPreventHibernation` | Passive logs/stats websockets do not reset idle deadlines |
| `TestGracePeriodCannotBypassExpiration` | Grace period is truncated if approaching `ExpiresAt` |
| `TestDataPreservation_WorkspaceRetainedAndRestorable` | Preserved files restore into a new sandbox; expired container retired |
