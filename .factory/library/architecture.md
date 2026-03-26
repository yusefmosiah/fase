# Architecture

Key architectural decisions and patterns for the cogent (formerly cogent) codebase.

**What belongs here:** Architectural decisions, discovered patterns, module boundaries.
**What does NOT belong here:** Service ports/commands (use `.factory/services.yaml`).

---

## Module Structure

- `cmd/cogent/` (→ `cmd/cogent/`) — CLI entry point (cobra)
- `internal/service/` — Core service (~9300 lines), work lifecycle, briefings, notifications
- `internal/store/` — SQLite persistence layer (~4000 lines)
- `internal/core/` — Types, constants, work states
- `internal/adapters/native/` — Active multi-LLM adapter (GLM, GPT, Claude, Gemini)
- `internal/adapters/*/` — Deprecated subprocess adapters (do not modify)
- `internal/notify/` — Email via Resend API, digest collector
- `internal/mcpserver/` — MCP server (tools being disabled, channel relay kept)
- `internal/cli/` — CLI commands, serve.go (HTTP/WS server, housekeeping)
- `mind-graph/` — Poincaré disk visualization UI
- `skills/` — Worker/checker skill markdown files (loaded at runtime)

## Key Patterns

- Work items form a graph (parent-child, dependency edges) stored in SQLite
- Jobs are execution attempts on work items (multiple jobs per work)
- Event bus (`EventBus`) for internal notifications
- Housekeeping loop in serve.go: 30s tick for WAL checkpoint, lease reconciliation, stall/orphan detection; hourly tick for digest flush
- Config from TOML file + environment variables
- State directory (`.cogent/` → `.cogent/`): SQLite DB, supervisor brief, raw stdout
