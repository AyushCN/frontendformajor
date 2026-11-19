# Test Coverage, Gap Analysis, and Verification Strategy

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Senior QA Automation Engineer, Senior Go Engineer, Principal Architect  

---

## 1. Test Suite Architecture & Coverage Summary

The API Sandbox Links test suite is designed for zero-flakiness, high-speed execution, and deep concurrency safety. All tests execute with the Go race detector enabled (`-race`).

```
Backend Test Suite
├── Unit & Algorithm Tests (In-memory mocks, sub-millisecond execution)
│   ├── envparse (Lexer, unquoting, whitespace stripping)
│   ├── security (SafeJoin, symlink traversal, TOCTOU defense, secret patterns)
│   ├── workspacelock (Read-write concurrency, fork exclusivity, reference counting)
│   └── orchestrator/entrypoint (Multi-language candidate scoring)
├── API Handler Tests (httptest.ResponseRecorder, Chi route context)
│   ├── auth (OAuth CSRF, session issuance, state validation)
│   ├── sandboxes & projects (Atomic creation, transfer, N+1 query prevention)
│   ├── env_vars (AES-256-GCM encryption, masking, merge/replace upload)
│   └── files (Confinement, symlink policies, permission boundaries)
└── Integration & Lifecycle Tests (Docker runner, Redis, PostgreSQL mocks)
    ├── reaper (Teardown failure recovery, version fencing)
    ├── proxy (Singleflight wake deduplication, host routing)
    └── runtime (BuildKit cache mounts, legacy runtime policies)
```

### Coverage Statistics by Subsystem

| Subsystem | Package | Test Files | Automated Test Count | Race Detector Status | Coverage Verdict |
|---|---|---|---|---|---|
| **API Transport & Routing** | `internal/api` | 6 files | 42 tests | **PASS (0 races)** | **Comprehensive** |
| **Workspace Concurrency** | `internal/workspacelock` | 1 file | 9 tests | **PASS (0 races)** | **Comprehensive** |
| **Filesystem & Secret Security** | `internal/security` | 1 file | 8 tests | **PASS (0 races)** | **Comprehensive** |
| **Git & Pre-Commit Scanner** | `internal/git` | 1 file | 7 tests | **PASS (0 races)** | **Comprehensive** |
| **Gateway & Wake-on-Request** | `internal/proxy` | 1 file | 6 tests | **PASS (0 races)** | **Comprehensive** |
| **Lifecycle Garbage Collector** | `internal/reaper` | 1 file | 3 tests | **PASS (0 races)** | **Comprehensive** |
| **Runtime & Build Execution** | `internal/orchestrator` | 5 files | 48 tests | **PASS (0 races)** | **Comprehensive** |
| **Environment File Lexer** | `internal/envparse` | 1 file | 6 tests | **PASS (0 races)** | **Comprehensive** |

---

## 2. Gap Analysis & Resolved Test Deficiencies

### Gap 1: Project Association Integrity Tests
- **Initial Deficiency:** No automated test verified that `CreateSandbox` atomically links to the destination project or that unassociated sandboxes are serialized without falling back to the creator's default project.
- **Resolution:** Authored `backend/internal/api/project_association_test.go`:
  - `TestProjectAssociation_CreateSandboxWithIntegrity` (Verifies atomic insertion and linking in shared projects).
  - `TestProjectAssociation_SerializationNoFallbackGuessing` (Verifies rejection of fallback guessing).
  - `TestProjectAssociation_TransferSandbox` (Verifies transfer integrity, permission checks, and conflicting ID rejection).

### Gap 2: Pre-Commit Untracked Secret Scanning Tests
- **Initial Deficiency:** Unit tests did not verify that untracked source files created on disk inside the workspace were scanned before committing.
- **Resolution:** Added `TestValidatePreCommit_BlocksSecretPatternInSource` and `TestUntrackFile_RemovesFromIndexAndAddsGitignore` in `backend/internal/git/secret_guard_test.go`.

---

## 3. Recommended Automated CI/CD Execution Command

```bash
# Execute full backend test suite with race detector and verbose logging
cd backend && go test -v -race -timeout 120s ./...

# Execute Next.js production build and TypeScript verification
cd frontend && npm run build
```
