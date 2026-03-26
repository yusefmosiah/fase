# Release Test Matrix

Date: 2026-03-10
Status: Draft

## Purpose

This matrix defines how `cogent` should be tested from cheap and deterministic
to expensive and emergent, with the default release lane biased toward low-cost
models and fake adapters.

## Test Lanes

1. Contract lane
   - Verify CLI help, JSON shape, exit codes, artifact layout, and stable command semantics.
   - No live model spend.

2. Fixture lane
   - Verify event translation and usage normalization from fake JSON streams.
   - No live model spend.

3. Fake orchestration lane
   - Verify background `run`, `send`, `debrief`, `transfer`, `cancel`,
     `status --wait`, `logs --follow`, and `artifacts`.
   - Includes recursive `cogent` orchestration through fake CLIs.
   - No live model spend.

4. Cheap live smoke lane
   - One trivial prompt per adapter using the lowest-cost accessible model.
   - Confirms launch, completion, session discovery, and basic usage reporting.

5. Cheap live workflow lane
   - Multi-step repo inspection, tool use, code edit, and verification tasks.
   - Prefer low-cost models like `glm-5`, `gpt-5-nano`, `gpt-5-mini`,
     `gemini-2.5-flash-lite`, or equivalent.

6. Recovery lane
   - Provider failure, explicit transfer, cancellation, and debrief workflows.

7. Capstone orchestration lane
   - Planner agent
   - Implementation agents
   - Verification loop
   - Review agent
   - Red-team agent
   - Security report agent

The capstone lane should exist in two forms:
- fake deterministic orchestration
- opt-in live orchestration with low-cost models

## Live Test Rules

- Live tests must be opt-in.
- Live tests should be parameterized by environment variables rather than
  hardcoded model names.
- Live tests should default to the cheapest usable model on each adapter.
- High-end models are comparison lanes, not the default regression lane.

## Observability Contract

`cogent` should report three separate things:

1. Runtime inventory
   - Installed adapter CLIs and capability flags.

2. Catalog inventory
   - Providers, models, auth mode, billing class, and best-effort pricing.

3. Job observability
   - Normalized token usage when exposed by the adapter.
   - Vendor-reported cost when exposed by the adapter.
   - Best-effort estimated cost only when a trustworthy provider/model pricing
     entry is available.

Pricing must carry provenance and should be treated as a routing hint, not an
authoritative billing ledger.
