# Supervisor Context (auto-generated)

This file contains a compressed summary of previous conversation turns.
It is automatically updated when history compression occurs.

## Context Summary

**Work ID:** `work_01KMFG3GC0PZVZTJ3KEHV2TGZS`
**Task:** Audit FASE API surface to identify the 20% that does 80% of the work and propose simplification.

### What Was Done
Comprehensive audit of the entire FASE API surface — CLI, HTTP, MCP server, native adapter tools, and web UI.

### Files Examined
- `internal/cli/root.go` — main CLI command registration (30 top-level commands)
- `internal/cli/serve.go` — `fase serve` implementation (HTTP server with 55+ endpoints)
- `internal/cli/check_and_report.go` — `fase check create` and `fase report` commands
- `internal/cli/dispatch.go` — `fase dispatch` command (single work item execution)
- `internal/cli/supervisor.go` — `fase supervisor` command (rotation-based supervision)
- `internal/cli/supervisor_agent.go` — agentic supervisor implementation
- `internal/cli/project.go` — `fase project` with `hydrate` subcommand
- `internal/cli/login.go` — `fase login` command
- `internal/cli/mcp.go` — `fase mcp` command (MCP proxy mode)
- `internal/adapters/native/tools_fase.go` — native adapter tools that call `fase` CLI
- `internal/adapters/native/tools.go` — tool registry (`Register()` method)
- `internal/adapters/native/adapter.go` — native adapter implementation
- `internal/mcpserver/server.go` — MCP server (15 tools exposed)
- `internal/mcpserver/tools_channel.go` — channel-specific MCP tools
- `mind-graph/src/lib/api.js` — web UI API client
- `skills/fase/SKILL.md` — worker skill briefing

### Key Findings
- **30 top-level CLI commands**, **90+ subcommands**, **55+ HTTP endpoints**, **15 MCP tools**, **8 native adapter tools**
- Only ~30% of CLI and ~36% of HTTP endpoints are essential
- MCP tools are well-curated (~80% essential)
- **Critical 20% (essential core):**
  - CLI: `fase serve`, `fase dispatch`, `fase work create/update/note/claim/list`, `fase check create`, `fase report`, `fase mcp`
  - HTTP: work CRUD (`/api/work/create`, `/api/work/update`, `/api/work/list`), dispatch (`/api/dispatch`), checks (`/api/check/create`), report (`/api/report`), supervisor status
  - MCP: `work_create`, `work_update`, `work_note`, `work_claim`, `work_list`, `dispatch`, `check_create`, `report`
  - Native adapter: `fase_work_update`, `fase_work_note`, `fase_work_claim`, `fase_check_create`, `fase_report`, `fase_dispatch`

### Redundancies Identified
1. `fase work check` duplicates `fase check create` — same endpoint
2. `/api/work/items` is legacy duplicate of `/api/work/list`
3. `fase runtime` overlaps `fase adapters`
4. `fase inbox` duplicates filtered `fase work list`

### Risks Noted
- Removing `/api/work/items` requires migrating web UI (`mind-graph/src/lib/api.js`) to `/api/work/list` first
- Web UI uses: `/api/work/items`, `/api/runs`, `/api/bash-log`, `/api/diff`, `/api/supervisor/status`, `/api/work/edges`

### Output Produced
- **`docs/api-surface-audit-80-20.md`** — Full audit document with detailed tables, categorization (essential/useful/dead-weight), and simplification proposal
- Work notes added via `fase work note-add` (3 notes: finding, finding, risk)
- Committed to git with descriptive message

### What Remains
- The audit document proposes simplification but no code changes have been made yet
- No decisions have been made on which endpoints to actually remove
- Web UI migration from `/api/work/items` → `/api/work/list` not started
- The work item is in "plan" kind — likely needs follow-up work items for actual implementation of simplifications
