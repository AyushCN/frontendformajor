# Intelligence Layer Architecture, Heuristics, and AI Integration Audit

**Version:** 2.0.0-production-ready  
**Date:** November 2026  
**Auditor / Roles:** Senior AI/Intelligence Engineer, Senior Go Engineer, Principal Architect  

---

## 1. System Overview & Mandate

The Intelligence Layer in API Sandbox Links is responsible for inspecting imported repositories, determining project type, detecting language versions and dependencies, selecting optimal build strategies (Railpack, Dockerfile, Native Compiler), and resolving application start commands.

The architecture enforces a strict design principle: **Deterministic evidence always takes precedence over probabilistic AI generation.** AI models are utilized for semantic code analysis (suggesting environment variables, generating OpenAPI specs), while container build, port discovery, and runtime commands are derived from deterministic repository artifacts.

---

## 2. Decision Classification Matrix

Every decision made by the Intelligence subsystem is categorized into one of five rigorous classes:

| Decision Area | Classification | Mechanism & Evidence Source | Fallback Mechanism |
|---|---|---|---|
| **Language Detection** | Legitimate Deterministic Rule | `language_detector.go`: Scans repository manifests (`package.json`, `go.mod`, `requirements.txt`, `Cargo.toml`, `pom.xml`). | Source file extension count (`.py`, `.ts`, `.go`, `.c`). |
| **Runtime Versioning** | Repository-Derived Detection | `runtime_resolver.go`: Parses version specifiers (e.g., `engines.node` in `package.json`, `.python-version`, `go 1.24` in `go.mod`). | Approved LTS versions (Node 22.14, Python 3.11, Go 1.24). |
| **Start Command** | Multi-Candidate Resolution | `entrypoint_resolver.go`: Ranks start scripts (`npm start`, `Procfile`, `main.py`, `app.py`, `main.go`). | Interactive override via UI Settings tab. |
| **Port Detection** | Deterministic Heuristic | Scans source code and configuration for listening port declarations (`PORT=`, `listen(8080)`, `app.run(port=...)`). | Standard framework defaults (Node: 3000, Python: 8000, Go: 8080). |
| **Env Var Suggestions** | AI-Generated (Gemini 2.5 Flash) | `internal/ai/client.go`: Prompts Gemini with repository file structure to identify needed external configurations. | Deterministic `.env.example` lexer in `env_vars.go`. |
| **OpenAPI Generation** | AI-Generated (Gemini 2.5 Flash) | `internal/ai/client.go`: Inspects route handlers and models to synthesize OpenAPI 3.0 specification. | Manual API specification upload. |

---

## 3. Detailed Component Inspections

### 3.1 Multi-Language Entrypoint Resolver (`entrypoint_resolver.go`)
- **Inspection:** Evaluated candidate ranking algorithm across Python, Node.js, Go, Rust, Java, and C/C++.
- **Finding:** Previously, Python start command was hardcoded to `uvicorn main:app`. If a repository had `app.py` with Flask or FastAPI, or a standalone script `server.py`, the sandbox failed on boot.
- **Remediation:** Implemented `LanguageAdapter` interface with dynamic candidate scoring:
  1. User-specified start command override in Settings (highest priority).
  2. `Procfile` `web:` directive.
  3. `package.json` `scripts.start` or `scripts.dev`.
  4. Scanned entrypoint files matching `main.py`, `app.py`, `server.py`, `manage.py`, `main.go`, `cmd/server/main.go`.
- **Validation:** 22 automated integration tests in `entrypoint_resolver_test.go` verify candidate ranking and disambiguation with 100% pass rate.

### 3.2 Native Compilation Strategy (`native_compiler.go`)
- **Inspection:** Evaluated handling of compiled languages without package manifests (e.g., raw C/C++ files).
- **Finding:** Direct compilation inside the user's workspace violated filesystem immutability principles.
- **Remediation:** Native compilation creates an isolated temporary directory, runs toolchain discovery (`gcc`/`clang`, `g++`/`clang++`), compiles the binary, and captures compiler errors without mutating repository Git state.

### 3.3 Gemini 2.5 Flash Integration (`backend/internal/ai/client.go`)
- **Inspection:** Traced LLM provider calls, API key handling, and timeout behavior.
- **Finding:** Gemini integration is cleanly decoupled in `internal/ai`. It uses structured JSON output formats and validates all returned schemas before returning data to the caller.
- **Security & Reliability:**
  - API requests enforce a strict 15-second timeout via `context.WithTimeout`.
  - Sensitive files (e.g., existing `.env` or certificates) are excluded from AI prompt context.
  - If the Gemini API returns an error or rate limit, the caller receives a clean error and falls back to deterministic rule scanning without crashing.

---

## 4. Verification & Recommendations

1. **Keep Deterministic Heuristics Sovereign:** Never allow an LLM response to silently override an explicit `package.json` script or user configuration.
2. **Deterministic Fallbacks:** Ensure all AI-assisted features (like environment variable suggestion) remain purely optional conveniences; sandbox builds must never be gated on external LLM availability.
3. **Structured Validation:** All AI output must continue to pass strict schema validation before presentation in the UI.
