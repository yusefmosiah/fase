# Contracts

Mission-facing contract notes for the readiness refactor.

**What belongs here:** canonical contract decisions, surface deprecations, and authoritative vocabulary once workers discover and confirm them.
**What does NOT belong here:** raw ADR text or long-form historical rationale already captured in repo docs.

---

- Canonical lifecycle, verification ownership, docs precedence, and surface vocabulary are expected to be frozen early in the mission and then treated as authoritative inputs for later workers.
- Any remaining deprecated names or divergent surfaces must be recorded here while they are being removed so later workers do not reintroduce them.

## Deprecated State Names

- `awaiting_attestation` is a deprecated alias for `checking`. It exists in `internal/core/types.go:294` for backward compatibility but `checking` is the canonical state. CLI help shows this deprecation. Do not use `awaiting_attestation` in new code or docs.

## Precedence Rule

- **Runtime code is canonical.** When docs, code, or persisted state disagree, code wins.
- Workers should look at `internal/core/types.go` for authoritative state definitions.
- Docs can be marked as non-canonical using a "Contract Note" blockquote at the top of the file.

## Event Provenance

- The `CreatedBy` field on events should use canonical values: `"supervisor"`, `"housekeeping"`, `"reconciler"`, `"worker"`.
- The `actorFromCreatedBy()` function in `internal/service/events.go` maps these to Actor types.
- Added `ActorHost` and `ActorService` mappings for complete provenance coverage.
- **Known gap:** MCP tool calls use `CreatedBy: "mcp"` which maps to `ActorMCP`. This is correct for external MCP clients, but when the supervisor triggers MCP mutations, the provenance should show `ActorSupervisor` instead of `ActorMCP`. Fixing this requires passing session context to the MCP server so it can distinguish supervisor sessions from external clients.

## Supervisor Wake Semantics

- The supervisor uses a **seen-set** mechanism (`map[WorkID]State`) to filter echo events and prevent self-wake loops.
- `RequiresSupervisorAttention()` in `internal/service/service.go` is the canonical wake gate.
- Idle suppression: 10-second backoff when `hasActionableWork()` returns false.
- Burst batching: 30-second debounce timer in `waitForSignal()`.
- Wake-relevant triggers: worker terminal, check recorded, attestation recorded, host manual action, housekeeping stall/orphan.
- Non-wake events: lease renew, worker progress without state change, job in-progress, claim change without state change, housekeeping noise.
