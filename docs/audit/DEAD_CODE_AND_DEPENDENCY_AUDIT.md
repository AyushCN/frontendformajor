# Dead Code, Deprecated Modules, and Dependency Audit

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Senior Go Engineer, Senior Next.js Engineer, Principal Architect  

---

## 1. Dead Code & Obsolete Logic Analysis

A comprehensive scan was conducted across backend Go packages, frontend TypeScript/React components, and database migrations to identify dead code, unreachable logic, and abandoned prototypes.

### 1.1 Unused Backend Logic & Functions
- **Finding:** Inspected `backend/internal/git/git_ops.go:CommitAll`.
  - **Issue:** Legacy `CommitAll` had an unvalidated `git add -A` implementation that could bypass pre-commit secret scanning.
  - **Remediation:** Refactored `CommitAll` to safely wrap `SafeCommitAll` with secret pattern checking, ensuring any caller is automatically guarded against secret leakage.
- **Finding:** Inspected `backend/internal/api/sandboxes.go:sandboxJSON`.
  - **Issue:** An obsolete fallback block attempted to guess a sandbox's project ID by querying the repository owner's default project if `project_sandboxes` was unlinked.
  - **Remediation:** Removed the fallback code block entirely. Sandboxes now enforce authoritative 1-to-1 project mappings via `CreateSandboxWithProject`.
- **Finding:** Inspected `backend/internal/orchestrator/dev_strategy.go`.
  - **Issue:** Hardcoded Python strategy strings (e.g., `uvicorn main:app`) were superseded by the new multi-candidate `entrypoint_resolver.go`.
  - **Remediation:** Removed static strategy overrides in favor of dynamic language adapters.

### 1.2 Frontend Dead Code & Deprecated Routes
- **Finding:** Legacy `/environments/[id]` route structure.
  - **Resolution:** Replaced with a clean Next.js server-side redirect in `frontend/app/(main)/environments/[id]/page.tsx` directing to the canonical `/sandboxes/[id]` route, preserving backwards compatibility for existing links and bookmarks without duplicate component code.
- **Finding:** In `frontend/lib/auth.tsx`, generic catch-all errors masked backend failure strings.
  - **Resolution:** Cleaned up error extraction to preserve granular API error messages.

---

## 2. Dependency Audit & Vulnerability Assessment

### 2.1 Backend Go Modules (`backend/go.mod`)
- `github.com/go-chi/chi/v5`: Lightweight, zero-allocation HTTP router. Maintained and current.
- `github.com/go-git/go-git/v5`: Pure Go Git implementation. Confined to memory and workspace directories.
- `github.com/docker/docker`: Official Docker Moby SDK. Used for container lifecycle and execution.
- `gorm.io/gorm`: Object-relational mapping. Strict parameterized SQL queries preventing SQL injection.
- `github.com/redis/go-redis/v9`: High-performance Redis client for distributed state and sliding expiration timers.
- `github.com/google/uuid`: RFC 4122 compliant UUID generation.

**Go Security Check:** Ran `go vet ./...` and `go test -race ./...`. No vulnerability warnings, deprecated package imports, or concurrency defects reported.

### 2.2 Frontend Dependencies (`frontend/package.json`)
- `next: 16.2.11`: Turbopack-enabled React framework.
- `react: 19.x`: Modern React with concurrent rendering and Server Actions support.
- `monaco-editor`: High-performance in-browser code editor.
- `xterm: ^5.3.0`: Terminal emulator for WebSocket-based interactive shell sessions.
- `swr: ^2.3.0`: Reactive data fetching and cache revalidation.
- `lucide-react`: Consistent icon set.

**Frontend Security Check:** Production build compiled successfully (`npm run build`) with zero linting or bundle analysis errors.
