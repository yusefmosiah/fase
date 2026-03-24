# FASE API Surface Audit: The 20% That Does 80% of the Work

## Executive Summary

FASE has **30 top-level CLI commands** with **90+ subcommands** and **55+ HTTP endpoints**. Analysis of actual usage across supervisors, workers, MCP tools, web UI, tests, and documentation reveals that **~15% of the surface handles ~85% of real traffic**.

The core workflow is: `serve → dispatch → work update → check → attest → report`. Everything else is support infrastructure, debugging aids, or experimental features.

---

## 1. CLI Command Inventory

### Top-Level Commands (30 total)

| # | Command | Kind | Used By | Verdict |
|---|---------|------|---------|---------|
| 1 | `serve` | Core | Human, supervisor | **ESSENTIAL** |
| 2 | `work` | Core | Supervisor, workers, humans | **ESSENTIAL** |
| 3 | `dispatch` | Core | Supervisor, humans | **ESSENTIAL** |
| 4 | `report` | Core | Workers (exit protocol) | **ESSENTIAL** |
| 5 | `check` | Core | Checkers, workers | **ESSENTIAL** |
| 6 | `run` | Core | Direct job launch | **ESSENTIAL** |
| 7 | `status` | Core | Workers, humans | **ESSENTIAL** |
| 8 | `mcp` | Core | Claude Code integration | **ESSENTIAL** |
| 9 | `version` | Utility | Everywhere | **ESSENTIAL** (trivial) |
| 10 | `list` | Support | Humans, debugging | SOMETIMES |
| 11 | `logs` | Support | Humans, debugging | SOMETIMES |
| 12 | `send` | Support | Supervisor continuation | SOMETIMES |
| 13 | `session` | Support | Debugging | SOMETIMES |
| 14 | `artifacts` | Support | Workers, debugging | SOMETIMES |
| 15 | `dashboard` | Support | Humans | SOMETIMES |
| 16 | `supervisor` | Legacy | Humans (pause/resume/send) | SOMETIMES |
| 17 | `project` | Support | Supervisor cold-start | SOMETIMES |
| 18 | `cancel` | Edge case | Manual intervention | RARELY |
| 19 | `debrief` | Edge case | Recovery/debugging | RARELY |
| 20 | `inbox` | Convenience | Humans (quick add) | RARELY |
| 21 | `reconcile` | Maintenance | Manual cleanup | RARELY |
| 22 | `adapters` | Discovery | Initial setup | RARELY |
| 23 | `catalog` | Discovery | Initial setup | RARELY |
| 24 | `history` | Debugging | Investigation | RARELY |
| 25 | `bootstrap` | Setup | Initial repo bootstrap | RARELY |
| 26 | `login` | Auth | ChatGPT OAuth setup | RARELY |
| 27 | `transfer` | Edge case | Cross-vendor failover | RARELY |
| 28 | `runtime` | Hidden | Internal | **NEVER (human)** |
| 29 | `__run-job` | Hidden | Internal worker | HIDDEN |
| 30 | `dash` (alias) | Alias | Humans | = dashboard |

### `fase work` Subcommands (30 total)

| Subcommand | Used By | Verdict |
|------------|---------|---------|
| `create` | Supervisor, workers, humans | **ESSENTIAL** |
| `show` | Supervisor, workers, humans | **ESSENTIAL** |
| `list` | Supervisor, humans | **ESSENTIAL** |
| `ready` | Supervisor, dispatch | **ESSENTIAL** |
| `update` | Workers (exit protocol) | **ESSENTIAL** |
| `note-add` | Workers, supervisor | **ESSENTIAL** |
| `attest` | Checkers, workers | **ESSENTIAL** |
| `claim` | Dispatch, supervisor | **ESSENTIAL** |
| `claim-next` | Dispatch | **ESSENTIAL** |
| `release` | Dispatch, supervisor | **ESSENTIAL** |
| `hydrate` | Supervisor, dispatch | **ESSENTIAL** |
| `notes` | Debugging | SOMETIMES |
| `children` | Supervisor, verification | SOMETIMES |
| `lock` | Humans (via web UI) | SOMETIMES |
| `unlock` | Humans (via web UI) | SOMETIMES |
| `approve` | Humans (via web UI) | SOMETIMES |
| `reject` | Humans (via web UI) | SOMETIMES |
| `check` | Workers (exit protocol) | **ESSENTIAL** |
| `private-note` | Workers (sensitive data) | SOMETIMES |
| `block` | Workers | SOMETIMES |
| `archive` | Maintenance | RARELY |
| `retry` | Recovery | RARELY |
| `renew-lease` | Workers | SOMETIMES |
| `discover` | Supervisor (child discovery) | RARELY |
| `verify` | Security audit | RARELY |
| `promote` | Deployment | RARELY |
| `doc-set` | Workers (doc coupling) | SOMETIMES |
| `proposal` (group) | Supervisor | RARELY |
| `projection` (group) | Debugging | RARELY |
| `edge` (group) | Supervisor | RARELY |

### `fase check` Subcommands (3 total)

| Subcommand | Used By | Verdict |
|------------|---------|---------|
| `create` | Checkers (MCP + CLI) | **ESSENTIAL** |
| `list` | Supervisor, debugging | **ESSENTIAL** |
| `show` | Supervisor | **ESSENTIAL** |

### Other Subcommand Groups

| Group | Subcommands | Verdict |
|-------|------------|---------|
| `fase mcp` | `stdio`, `http`, `proxy` | `stdio` + `proxy` **ESSENTIAL**, `http` RARELY |
| `fase login` | `chatgpt`, `status` | RARELY |
| `fase catalog` | `sync`, `show`, `probe` | RARELY |
| `fase artifacts` | `list`, `attach`, `show` | SOMETIMES |
| `fase history` | `search` | RARELY |
| `fase bootstrap` | `inspect`, `create` | RARELY |
| `fase transfer` | `export`, `run` | RARELY |
| `fase supervisor` | `pause`, `resume`, `send` | SOMETIMES (human control) |

---

## 2. HTTP API Endpoint Inventory

### Core Work Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/work/create` | POST | CLI, MCP, supervisor | **ESSENTIAL** |
| `/api/work/{id}` | GET | CLI, MCP, web UI | **ESSENTIAL** |
| `/api/work/list` | GET | CLI, MCP, supervisor | **ESSENTIAL** |
| `/api/work/ready` | GET | CLI, dispatch, supervisor | **ESSENTIAL** |
| `/api/work/{id}/update` | POST | CLI, MCP, workers | **ESSENTIAL** |
| `/api/work/{id}/note-add` | POST | CLI, MCP, workers | **ESSENTIAL** |
| `/api/work/{id}/notes` | GET | CLI, MCP | SOMETIMES |
| `/api/work/{id}/attest` | POST | CLI, MCP, checkers | **ESSENTIAL** |
| `/api/work/{id}/claim` | POST | CLI, MCP, dispatch | **ESSENTIAL** |
| `/api/work/{id}/claim-next` | POST | CLI, dispatch | **ESSENTIAL** |
| `/api/work/{id}/release` | POST | CLI, dispatch | **ESSENTIAL** |
| `/api/work/{id}/hydrate` | GET | CLI, dispatch, supervisor | **ESSENTIAL** |
| `/api/work/{id}/check` | POST | CLI, workers | **ESSENTIAL** |
| `/api/work/{id}/children` | GET | CLI | SOMETIMES |
| `/api/work/{id}/lock` | POST | CLI, web UI | SOMETIMES |
| `/api/work/{id}/unlock` | POST | CLI, web UI | SOMETIMES |
| `/api/work/{id}/approve` | POST | CLI, web UI | SOMETIMES |
| `/api/work/{id}/reject` | POST | CLI, web UI | SOMETIMES |
| `/api/work/{id}/block` | POST | CLI | RARELY |
| `/api/work/{id}/archive` | POST | CLI | RARELY |
| `/api/work/{id}/retry` | POST | CLI | RARELY |
| `/api/work/{id}/renew-lease` | POST | CLI | RARELY |
| `/api/work/{id}/discover` | POST | CLI | RARELY |
| `/api/work/{id}/verify` | POST | CLI | RARELY |
| `/api/work/{id}/promote` | POST | CLI, web UI | RARELY |
| `/api/work/{id}/private-note` | POST | CLI | SOMETIMES |
| `/api/work/{id}/doc-set` | POST | CLI | SOMETIMES |
| `/api/work/items` | GET | Web UI (legacy) | SOMETIMES (legacy) |

### Check Record Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/check/create` | POST | CLI | **ESSENTIAL** |
| `/api/check/list` | GET | CLI | **ESSENTIAL** |
| `/api/check/show` | GET | CLI | **ESSENTIAL** |

### Job/Session Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/job/run` | POST | CLI, supervisor | **ESSENTIAL** |
| `/api/job/send` | POST | CLI, supervisor | SOMETIMES |
| `/api/job/list` | GET | CLI | SOMETIMES |
| `/api/job/{id}/status` | GET | CLI | SOMETIMES |
| `/api/job/{id}/logs` | GET | CLI | SOMETIMES |
| `/api/job/{id}/logs-after` | GET | CLI (internal) | RARELY |
| `/api/job/{id}/logs-raw` | GET | CLI | RARELY |
| `/api/job/{id}/logs-raw-after` | GET | CLI (internal) | RARELY |
| `/api/job/{id}/cancel` | POST | CLI | RARELY |
| `/api/session/list` | GET | CLI | SOMETIMES |
| `/api/session/{id}` | GET | CLI | SOMETIMES |
| `/api/debrief` | POST | CLI | RARELY |

### Dispatch/Supervisor Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/dispatch` | POST | CLI `dispatch` | **ESSENTIAL** |
| `/api/supervisor/status` | GET | Web UI | SOMETIMES |
| `/api/supervisor/pause` | POST | CLI | SOMETIMES |
| `/api/supervisor/resume` | POST | CLI | SOMETIMES |
| `/api/supervisor/send` | POST | CLI | SOMETIMES |
| `/api/channel/send` | POST | CLI `report`, supervisor | **ESSENTIAL** |

### Artifact/History Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/artifact/list` | GET | CLI | SOMETIMES |
| `/api/artifact/attach` | POST | CLI | SOMETIMES |
| `/api/artifact/{id}` | GET | CLI | SOMETIMES |
| `/api/history/search` | GET | CLI | RARELY |

### Proposal/Edge Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/proposal/create` | POST | CLI | RARELY |
| `/api/proposal/list` | GET | CLI | RARELY |
| `/api/proposal/{id}` | GET | CLI | RARELY |
| `/api/proposal/{id}/accept` | POST | CLI | RARELY |
| `/api/proposal/{id}/reject` | POST | CLI | RARELY |
| `/api/work/edges` | GET | Web UI | SOMETIMES |
| `/api/work/edges/add` | POST | CLI | RARELY |
| `/api/work/edges/rm` | POST | CLI | RARELY |

### Infrastructure Endpoints

| Endpoint | Method | Callers | Verdict |
|----------|--------|---------|---------|
| `/api/dashboard` | GET | CLI `dashboard` | SOMETIMES |
| `/api/reconcile` | POST | CLI, housekeeping | SOMETIMES |
| `/api/adapters` | GET | CLI | RARELY |
| `/api/runtime` | GET | CLI | RARELY |
| `/api/catalog/sync` | POST | CLI | RARELY |
| `/api/catalog/show` | GET | CLI | RARELY |
| `/api/catalog/probe` | POST | CLI | RARELY |
| `/api/transfer/export` | POST | CLI | RARELY |
| `/api/transfer/run` | POST | CLI | RARELY |
| `/api/bootstrap/inspect` | POST | CLI | RARELY |
| `/api/bootstrap/create` | POST | CLI | RARELY |
| `/api/project/hydrate` | GET | CLI, supervisor | SOMETIMES |
| `/api/attestation/{id}/sign` | POST | CLI `attest` | SOMETIMES |
| `/api/internal/run-job` | POST | `__run-job` subprocess | **ESSENTIAL** (internal) |
| `/api/runs` | GET | Web UI | SOMETIMES |
| `/api/runs/{id}` | GET | Web UI | SOMETIMES |
| `/api/git/status` | GET | Web UI (unused?) | RARELY |
| `/api/diff` | GET | Web UI | SOMETIMES |
| `/api/bash-log` | GET | Web UI | SOMETIMES |

### WebSocket/MCP

| Endpoint | Protocol | Callers | Verdict |
|----------|----------|---------|---------|
| `/ws` | WebSocket | MCP proxy, web UI | **ESSENTIAL** |
| `/mcp` | HTTP+SSE | MCP clients | **ESSENTIAL** |

---

## 3. MCP Tool Inventory (15 tools)

| Tool | Used By | Verdict |
|------|---------|---------|
| `project_hydrate` | Supervisor, workers | **ESSENTIAL** |
| `work_list` | Supervisor | **ESSENTIAL** |
| `work_show` | Supervisor, checkers | **ESSENTIAL** |
| `work_notes` | Supervisor | SOMETIMES |
| `work_update` | Supervisor, workers | **ESSENTIAL** |
| `work_create` | Supervisor | **ESSENTIAL** |
| `work_note_add` | Supervisor, workers | **ESSENTIAL** |
| `work_attest` | Checkers | **ESSENTIAL** |
| `work_claim` | Dispatch | **ESSENTIAL** |
| `ready_work` | Supervisor | **ESSENTIAL** |
| `check_record_create` | Checkers | **ESSENTIAL** |
| `check_record_show` | Supervisor | **ESSENTIAL** |
| `check_record_list` | Supervisor | **ESSENTIAL** |
| `session_send` | Supervisor (steering) | SOMETIMES |
| `send_escalation_email` | Supervisor | RARELY |

---

## 4. Native Adapter Worker Tools (8 tools)

| Tool | Used By | Verdict |
|------|---------|---------|
| `check_record_create` | Checkers (native sessions) | **ESSENTIAL** |
| `check_record_list` | Checkers | **ESSENTIAL** |
| `check_record_show` | Checkers | **ESSENTIAL** |
| `run_tests` | Checkers | **ESSENTIAL** |
| `run_playwright` | Checkers | **ESSENTIAL** |
| `bash` | Workers | **ESSENTIAL** |
| `write_file` | Workers | **ESSENTIAL** |
| `read_file` | Workers | **ESSENTIAL** |
| `web_search` | Workers (conditional) | SOMETIMES |
| `web_fetch` | Workers (conditional) | SOMETIMES |

---

## 5. Redundancy and Dead Weight

### Redundant Pairs

| Surface A | Surface B | Relationship | Recommendation |
|-----------|-----------|--------------|----------------|
| `fase work check` | `fase check create` | Both submit check records | **Merge**: keep `fase check create` as canonical, deprecate `fase work check` |
| `fase work items` endpoint | `fase work list` endpoint | Legacy vs canonical list | **Remove** `/api/work/items` (web UI should use `/api/work/list`) |
| `fase report` | `fase work update --message` | Both post messages | Keep both — `report` goes through channel, `update` mutates state |
| `fase supervisor send` | `fase serve --auto` supervisor steering | Both send to supervisor | Keep `supervisor send` as human control surface |
| `fase runtime` (hidden) | `fase adapters` | Overlapping adapter info | **Merge** into `fase adapters` |

### Commands with Zero/Minimal Callers

| Command | Why It Exists | Recommendation |
|---------|---------------|----------------|
| `fase work projection` | Debug projections (checklist, status) | **Remove** — `work show` + `hydrate` cover this |
| `fase work proposal` | Structural graph edits | **Keep** but hide — used by supervisor for child work |
| `fase work edge` | DAG management | **Keep** but hide — supervisor uses internally |
| `fase work verify` | Cryptographic audit | **Keep** — security feature |
| `fase work doc-set` | Doc-work coupling | **Keep** — ADR-0002 compliance |
| `fase work promote` | Deployment promotion | **Keep** — future CI/CD integration |
| `fase history search` | Full-text search | **Keep** but low priority |
| `fase transfer` | Cross-vendor failover | **Keep** — recovery feature |
| `fase login` | Auth flows | **Keep** — needed for ChatGPT |
| `fase debrief` | Session self-assessment | **Keep** — recovery workflow |
| `fase cancel` | Job cancellation | **Keep** — manual intervention |
| `fase bootstrap` | Repo onboarding | **Keep** — initial setup |

### HTTP Endpoints with No Direct Callers (web-only or unused)

| Endpoint | Status |
|----------|--------|
| `/api/git/status` | **Likely unused** — web UI doesn't appear to call it |
| `/api/job/{id}/logs-after` | Internal only (follow mode) |
| `/api/job/{id}/logs-raw-after` | Internal only (follow mode) |

---

## 6. Proposed Minimal API Surface

### Essential CLI (9 commands → handles 85% of traffic)

```
fase serve              # Start the service
fase dispatch           # Dispatch ready work
fase work create        # Create work items
fase work show          # Inspect work state
fase work list          # List work items
fase work update        # Update work state (worker exit)
fase work note-add      # Record findings
fase work hydrate       # Generate briefings
fase check create       # Submit check records
fase check list         # Review checks
fase report             # Notify supervisor/host
fase mcp stdio/proxy    # Claude Code integration
fase version            # Version info
```

### Essential HTTP Endpoints (20 endpoints → handles 90% of traffic)

```
POST   /api/work/create              # Create work
GET    /api/work/{id}                # Show work
GET    /api/work/list                # List work
GET    /api/work/ready               # Ready work
POST   /api/work/{id}/update         # Update state
POST   /api/work/{id}/note-add       # Add note
POST   /api/work/{id}/attest         # Attest work
POST   /api/work/{id}/claim          # Claim work
POST   /api/work/{id}/claim-next     # Claim next ready
POST   /api/work/{id}/release        # Release claim
GET    /api/work/{id}/hydrate        # Hydrate briefing
POST   /api/work/{id}/check          # Submit check
POST   /api/dispatch                 # Dispatch work
POST   /api/check/create             # Create check record
GET    /api/check/list               # List checks
GET    /api/check/show               # Show check
POST   /api/channel/send             # Report/notify
POST   /api/internal/run-job         # Worker execution
POST   /api/job/run                  # Launch job
GET    /api/project/hydrate          # Supervisor briefing
```

### MCP Tools (11 essential)

```
project_hydrate       # Supervisor briefing
work_list             # List work
work_show             # Show work
work_update           # Update work
work_create           # Create work
work_note_add         # Add notes
work_attest           # Attest work
work_claim            # Claim work
ready_work            # List ready work
check_record_create   # Submit check
check_record_show     # Show check
check_record_list     # List checks
```

---

## 7. What to Remove/Merge

### Remove (Dead Weight)

1. **`fase work projection`** subcommand group — `work show` + `hydrate` already render all projections
2. **`/api/work/items`** — legacy duplicate of `/api/work/list`; migrate web UI
3. **`/api/git/status`** — unused by web UI
4. **`fase work check`** — merge into `fase check create` (same endpoint, duplicate CLI path)

### Merge/Consolidate

1. **`fase runtime`** → merge into **`fase adapters`** (add `--runtime` flag)
2. **`fase work check`** → use **`fase check create`** only
3. **`/api/work/items`** → redirect to **`/api/work/list`**

### Hide (Keep but Mark Hidden)

1. `fase __run-job` — already hidden, keep
2. `fase work edge` — DAG internals, not user-facing
3. `fase work proposal` — supervisor-internal

### Demote to Second Tier (Keep but De-prioritize)

1. `fase catalog` — useful for initial setup only
2. `fase history` — investigation tool, rarely needed
3. `fase bootstrap` — one-time repo setup
4. `fase login` — one-time auth
5. `fase transfer` — recovery edge case
6. `fase debrief` — recovery edge case
7. `fase cancel` — manual intervention

---

## 8. Usage Frequency Summary

| Category | Total | Essential | Sometimes | Rarely |
|----------|-------|----------|----------|--------|
| Top-level CLI commands | 30 | 9 (30%) | 8 (27%) | 13 (43%) |
| `fase work` subcommands | 30 | 13 (43%) | 8 (27%) | 9 (30%) |
| HTTP endpoints | 55+ | 20 (36%) | 12 (22%) | 23+ (42%) |
| MCP tools | 15 | 12 (80%) | 2 (13%) | 1 (7%) |
| Native adapter tools | 8 | 7 (88%) | 1 (12%) | 0 |

**Key insight**: The MCP tool surface is already well-curated (80% essential). The CLI and HTTP surface have accumulated significant dead weight from exploration and feature flags that were never adopted.

---

## 9. The Simplest CLI Surface

If FASE were rebuilt from scratch supporting only the core workflow (create work → dispatch → work → check → attest → report), the CLI would be:

```
fase serve                    # Run the service
fase dispatch [work-id]       # Dispatch work
fase work create              # Create work
fase work show <id>           # Inspect work
fase work list                # List work
fase work update <id>         # Update state
fase work note-add <id>       # Record findings
fase check create <id>        # Submit check
fase check list <id>          # Review checks
fase report "<message>"       # Notify host
fase mcp proxy                # Claude Code bridge
```

**12 commands.** Everything else (lock/unlock, approve/reject, proposals, edges, projections, catalog, transfer, debrief, bootstrap, login, history, artifacts) can be added back incrementally when a concrete user need arises.

The HTTP surface maps 1:1 to these commands plus a few internal endpoints (`/api/internal/run-job`, `/api/channel/send`, `/ws`, `/mcp`).

---

*Research completed: 2026-03-24. Work ID: work_01KMFG3GC0PZVZTJ3KEHV2TGZS*
