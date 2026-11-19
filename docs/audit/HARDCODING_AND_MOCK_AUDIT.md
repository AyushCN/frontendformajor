# Complete Hardcoding, Mock Data, and Static Implementation Audit

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Principal Software Architect, Senior Go Engineer, DevOps Engineer, Application Security Engineer  

---

## 1. Executive Summary

This audit systematically examined the entire API Sandbox Links repository for inappropriate hardcoded URLs, ports, filesystem paths, user IDs, credentials, mock authentication, static API responses, and fake business data. 

**Core Verification Findings:**
1. **Zero Fake Users or Seed Accounts:** The production and development database migrations and source files contain zero hardcoded test accounts, mock users, or fake developer profiles. Empty database states are handled gracefully with proper zero-state UI messaging.
2. **Centralized Configuration:** System ports, gateway hostnames, database URLs, Redis addresses, and timeouts are centralized in `backend/internal/config/config.go` with environment variable overrides.
3. **No Fallback Guessing:** The backend `sandboxJSON` serializer no longer falls back to assigning unassociated sandboxes to the repository owner's default project.
4. **Legitimate Constants Preserved:** Invariant protocol defaults (such as default 15-minute inactivity timeouts, Docker network names, standard HTTP headers, and package manager cache paths) are preserved as typed constants with clear configuration overrides.

---

## 2. Hardcoding Audit Register

| Finding ID | Subsystem | File & Location | Identified Hardcoding | Action Taken | Current Classification |
|---|---|---|---|---|---|
| **HC-001** | Backend API | `sandboxes.go:sandboxJSON` | Repository owner default project fallback when `project_sandboxes` entry was missing. | **REMOVED.** Sandbox serialization strictly requires authoritative DB project mapping. | Clean Dynamic DB Relation |
| **HC-002** | Gateway Proxy | `wake_middleware.go` | Hostname `.sandbox.localhost` hardcoded in port rewrites without configurable base domain. | **CENTRALIZED.** Derived dynamically from `cfg.BaseDomain` with fallback for local dev. | Configurable Parameter |
| **HC-003** | Frontend BFF | `lib/auth.tsx:121` | Generic fallback string `"An error occurred"` swallowed backend error messages. | **FIXED.** Updated to extract `data.error || data.message || res.statusText`. | Proper Error Propagation |
| **HC-004** | Orchestrator | `dev_strategy.go` | Uvicorn start command hardcoded `uvicorn main:app` without inspecting `app.py`. | **FIXED.** Replaced with multi-candidate `entrypoint_resolver.go` prioritizing detected source files. | Dynamic Detection Rule |
| **HC-005** | Config Loader | `internal/config` | Hardcoded default Redis and PostgreSQL connection strings in multiple files. | **CENTRALIZED.** Single source of truth in `internal/config/config.go` with `.env` injection. | Centralized Configuration |
| **HC-006** | Git Security | `secret_guard.go` | Hardcoded file list for `.env` scanning was limited to only 4 exact filenames. | **EXPANDED.** Comprehensive regex matching covering all variants (`.env.example`, `.env.sample`, `.env.template`, etc.). | Deterministic Invariant Rule |
| **HC-007** | Frontend UI | `EnvironmentSettings.tsx` | Fixed `max-w-3xl` container width limiting card responsiveness. | **REMOVED.** Full width `w-full` applied with standard dynamic viewport sizing. | Responsive Layout |

---

## 3. Mock Implementations & Fake Data Audit

### 3.1 Authentication & User Identity
- **Finding:** Inspected `backend/internal/api/auth.go` and `frontend/lib/auth.tsx`.
- **Status:** **CLEAN.** No mock GitHub OAuth logins or fake user tokens exist in production code paths.
- **Development Mode (`DEV_LOGIN_ENABLED`):** If explicitly enabled via environment variable in non-production environments, a development endpoint exists for local testing, but it is strictly disabled if `ENVIRONMENT=production` or `ENV=prod`. Production environments require authentic GitHub OAuth 2.0 exchange with state cookie validation.

### 3.2 Database Seeding & Mock Data
- **Finding:** Inspected SQL migrations `0001` through `0012` and `cmd/migrate/main.go`.
- **Status:** **CLEAN.** Migrations define strict relational DDL (tables, indexes, foreign keys, constraints). No `INSERT` statements insert sample or fake tenant data.
- **Empty State Behavior:** When a new user signs in, an authoritative default project is created atomically in PostgreSQL. If the project contains no repositories or sandboxes, the UI renders clean empty states with guided onboarding links instead of placeholder data.

### 3.3 Intelligence Layer Mock Responses
- **Finding:** Inspected `backend/internal/ai/client.go` and `backend/internal/orchestrator/analyzer.go`.
- **Status:** **CLEAN.** The Gemini client performs authentic API requests to Google AI services using `GEMINI_API_KEY`. If the key is omitted or the service is unreachable, it logs a warning and gracefully yields to deterministic local static analysis. No simulated mock AI outputs are emitted in production.

---

## 4. Legitimate Constants & Protocol Invariants

The following constants were reviewed and determined to be legitimate invariant protocol defaults rather than problematic hardcoding:

1. **Default Inactivity Timeout (900 seconds / 15 minutes):** Industry-standard idle timeout balance between cloud resource conservation and developer experience. Configurable per sandbox in Settings.
2. **Default Workspace Retention (604,800 seconds / 7 days):** Guaranteed disk storage window for user code after container expiration.
3. **Container Resource Caps (1GB RAM, 1.0 CPU Core):** Safety limits preventing runaway sandbox workloads from exhausting host resources. Configurable via server environment flags.
4. **Protected Environment Filenames (`.env`, `.env.local`, `.env.*.local`):** Security invariant defining files that must never be committed to Git.
