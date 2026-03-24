# Model Selection Strategy

**Status**: Current as of 2026-03-22. Update when provider budgets change.

## Overview

FASE dispatches work to multiple AI providers. Different roles require different capabilities and cost profiles. This document defines which models to use for each role, how to handle provider depletion, and where the selection logic lives in code.

## Provider Budget (2026-03-22)

| Provider | Limit | Reset | Status |
|----------|-------|-------|--------|
| z.ai (GLM) | Near-unlimited | Rolling | **Use freely** — primary worker pool |
| Claude (claude.ai) | Weekly | ~tomorrow | **Use for implementation** — haiku for workers, sonnet for complex, opus for attestation |
| OpenAI (ChatGPT) | 26% remaining | 5 days | **CONSERVE** — supervisor only (gpt-5.4-mini), no workers |
| Bedrock (AWS) | Pay-per-use | None | **Fallback** — use when claude.ai depleted |

## Role Assignments

### Implementation Workers

Dispatched by the supervisor to implement code, fix bugs, or refactor.

| Priority | Adapter | Model | When to use |
|----------|---------|-------|-------------|
| 1 (primary) | native | zai/glm-5-turbo | Always — unlimited, fast, good for most implementation |
| 2 | claude | claude-haiku-4-5 | When GLM output quality is insufficient |
| 3 | claude | claude-sonnet-4-6 | Complex work: architecture, multi-file refactors, new subsystems |
| 4 | opencode | zai-coding-plan/glm-5-turbo | Alternative for GLM with planning emphasis |

**Rule**: Do not use OpenAI workers. Conserve gpt-5.4-mini for supervisor only.

### Supervisor

The agentic supervisor (ADR-0041) runs as a long-lived session managing the work queue.

**Default**: `claude-sonnet-4-6` via `claude` adapter.

**Why not gpt-5.4?** Rate limit conservation. The supervisor runs many turns; sonnet is cheaper and sufficient for queue management and dispatch decisions.

**Fallback sequence** when claude.ai is rate-limited:
1. `native/chatgpt/gpt-5.4-mini` — cheapest option, just queue management
2. `codex/gpt-5.4-mini` — if native chatgpt is unavailable
3. Pause + notify host

**CLI**: `fase serve --auto --supervisor-adapter claude --supervisor-model claude-sonnet-4-6`

### Planning / Design

Work items of kind `plan` that require deep reasoning, architecture decisions, or complex decomposition.

| Adapter | Model | When to use |
|---------|-------|-------------|
| claude | claude-opus-4-6 (via claude.ai) | When claude.ai budget available |
| native | chatgpt/gpt-5.4 | When claude.ai depleted and decision quality matters |
| native | bedrock/claude-opus-4-6 | Bedrock fallback for deep reasoning |

### E2E Attestation (Playwright)

Attestation workers that verify UI behavior, run Playwright tests, and review screenshots. **Requires multimodal capability** (GLM models cannot do this).

| Priority | Adapter | Model | Notes |
|----------|---------|-------|-------|
| 1 | claude | claude-opus-4-6 | Best judgment + vision |
| 2 | native | chatgpt/gpt-5.4 | If claude.ai depleted |
| 3 | native | bedrock/claude-opus-4-6 | Pay-per-use Bedrock fallback |
| ❌ | native | zai/glm-* | Cannot — text-only, no vision |

### Non-E2E Attestation

Code review, test verification, static analysis — no visual output required.

| Priority | Adapter | Model | Notes |
|----------|---------|-------|-------|
| 1 | claude | claude-haiku-4-5 | Fast, cheap, sufficient for code review |
| 2 | native | zai/glm-5-turbo | When haiku is depleted |
| 3 | native | bedrock/claude-haiku-4-5 | Bedrock fallback |

### Checker Pool (Automated Attestation)

The automated checker pool (`dispatchChecker` in `service/service.go:2827`) selects a model that **differs** from the worker model to provide independent verification.

```go
// internal/service/service.go
var checkerModels = []struct{ adapter, model string }{
    {"claude", "claude-opus-4-6"},
    {"claude", "claude-sonnet-4-6"},
    {"native", "bedrock/claude-sonnet-4-6"},
}
```

All checkers use Claude adapters (MCP tool access required for `check_record_create`). GLM cannot be a checker.

## Dispatch Selection Logic

Worker dispatch is implemented in `internal/cli/supervisor.go`. The selection priority:

1. **Work item `preferred_adapters` + `preferred_models`** — explicit override from work item metadata
2. **Work item `preferred_models`** (no adapter) — find matching model in rotation pool
3. **Round-robin avoiding last adapter** — rotate away from the previous job's adapter
4. **Default adapter hint** — fallback when no history exists
5. **Global round-robin** — atomic counter across all dispatches

The rotation pool (`workRotation` in `supervisor.go:23`):
```go
var workRotation = []rotationEntry{
    {adapter: "native", model: "chatgpt/gpt-5.4-mini"},
    {adapter: "native", model: "zai/glm-5-turbo"},
    {adapter: "native", model: "bedrock/claude-haiku-4-5"},
    {adapter: "codex", model: "gpt-5.4"},
    {adapter: "codex", model: "gpt-5.4-mini"},
    {adapter: "claude", model: "claude-sonnet-4-6"},
    {adapter: "claude", model: "claude-haiku-4-5"},
    {adapter: "opencode", model: "zai-coding-plan/glm-5-turbo"},
}
```

**Note**: This rotation includes OpenAI models. The supervisor should apply role-based overrides (see below) to avoid burning OpenAI budget on workers.

## Provider Failover

When a provider is depleted, the supervisor (acting as queue manager) should:

1. **Detect depletion**: rate-limit errors show as failed jobs or error turn outcomes
2. **Apply fallback**: route work to the next available provider in priority order
3. **Notify host**: escalate via `notifyHost()` if all providers for a role are depleted

**Failover chains by role:**

```
Worker:         glm-5-turbo → claude-haiku → bedrock/claude-haiku → sonnet (expensive)
Supervisor:     claude-sonnet → gpt-5.4-mini → pause+notify
E2E Attestation: claude-opus → gpt-5.4 → bedrock/claude-opus → block (cannot degrade)
Planning:       claude-opus → gpt-5.4 → bedrock/claude-opus
```

**Current gap**: Provider failover is not yet automated in code. The supervisor LLM applies failover via prompt instructions. A `work_01KMC3TFM90ZXXTZZV4ESP417N` fix item exists for stall detection; provider failover should be tracked separately.

## Model Capabilities Reference

| Model | Vision/Multimodal | Can run Playwright | MCP tools | Notes |
|-------|-------------------|--------------------|-----------|-------|
| zai/glm-5-turbo | No | No | Native ToolRegistry | Unlimited, primary worker |
| zai/glm-5 | No | No | Native ToolRegistry | Higher quality than glm-5-turbo |
| zai/glm-4.7 | No | No | Native ToolRegistry | |
| claude-haiku-4-5 | Yes | Yes | MCP (claude adapter) | Cheap, fast, multimodal |
| claude-sonnet-4-6 | Yes | Yes | MCP (claude adapter) | Balanced cost/quality |
| claude-opus-4-6 | Yes | Yes | MCP (claude adapter) | Best reasoning, most expensive |
| bedrock/claude-haiku-4-5 | Yes | Yes | Native ToolRegistry | Pay-per-use, good fallback |
| bedrock/claude-sonnet-4-6 | Yes | Yes | Native ToolRegistry | Pay-per-use |
| bedrock/claude-opus-4-6 | Yes | Yes | Native ToolRegistry | Pay-per-use |
| chatgpt/gpt-5.4 | Yes | Yes | Native ToolRegistry | Strong reasoning, CONSERVE |
| chatgpt/gpt-5.4-mini | Yes | Yes | Native ToolRegistry | Cheap, supervisor only |
| codex/gpt-5.4 | Yes | Yes | Codex subprocess | |
| codex/gpt-5.4-mini | Yes | Yes | Codex subprocess | |

## Cost Guidance

From the 2026-03-22 overnight run (~$38 for 19K lines):

| Component | Cost | Notes |
|-----------|------|-------|
| Supervisor (~100 turns, gpt-5.4-mini) | ~$5 | Keep supervisor cheap |
| Workers (glm-5-turbo, ~30 dispatches) | ~$10 | Primary worker cost |
| Workers (claude-haiku, ~15 dispatches) | ~$8 | Secondary workers |
| Workers (gpt-5.4-mini, ~10 dispatches) | ~$5 | Should avoid — use GLM instead |
| Workers (claude-sonnet, ~5 dispatches) | ~$10 | Complex work only |

**Key insight**: GLM workers are 3-5x cheaper than Claude/OpenAI for equivalent implementation work. Default to GLM; escalate to Claude for quality-sensitive tasks.

## Work Item Annotations

To route a specific work item to a model, set `preferred_adapters` / `preferred_models` via `fase work update`:

```bash
# Route to Claude sonnet for complex work
fase work update <id> --preferred-adapters claude --preferred-models claude-sonnet-4-6

# Route to GLM for bulk implementation
fase work update <id> --preferred-adapters native --preferred-models zai/glm-5-turbo
```

The dispatch logic respects these fields (priority #1 in selection).

## Open Questions

1. **Automated failover**: Should `dispatchChecker` / the supervisor loop detect rate-limit errors and retry with a different model? Currently the supervisor LLM handles this via instruction, but a Go-level circuit breaker would be more reliable.
2. **OpenAI in workRotation**: The rotation pool includes `chatgpt/gpt-5.4-mini` as a worker. Should it be removed from the pool and reserved only for the supervisor? This would require updating `workRotation` in `supervisor.go`.
3. **Bedrock as default**: Bedrock is pay-per-use with no hard limit. Should it be promoted higher in the rotation as a safety net when cloud provider limits are hit?
