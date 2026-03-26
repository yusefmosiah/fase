---
name: contract-alignment-worker
description: Align repo docs, guidance, and contract-facing surfaces to one canonical Cogent readiness contract.
---

# Contract Alignment Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the work procedure.

## When to Use This Skill

Use this skill for readiness features that primarily align:

- README / ADR / spec / guidance documents
- runtime help or text-facing contract surfaces
- doc-linking semantics and proof-bundle reporting language
- duplication removal across contract-facing docs or briefing artifacts

## Required Skills

- None

## Work Procedure

1. Read the assigned feature plus `mission.md`, `AGENTS.md`, and the exact assertion IDs listed in `fulfills`.
2. Inventory every repo-facing source that states the relevant contract. Do not update only one copy of the truth.
3. If the feature depends on runtime behavior that is not yet implemented, stop and return to the orchestrator instead of writing speculative final-state docs.
4. For code-adjacent changes (for example runtime help text or reporting payload labels), add or update focused tests first where practical.
5. Make the smallest set of edits needed to leave one clear contract and remove or demote contradictory copies.
6. Run the relevant focused tests, then:
   - `make lint`
   - `make build`
7. In the handoff, enumerate which conflicting sources were aligned, removed, or marked historical, and call out any remaining surfaces that still need a later runtime feature to fully satisfy the contract.

## Example Handoff

```json
{
  "salientSummary": "Aligned the repo’s lifecycle/verification documentation to one explicit precedence rule and removed the stale duplicate checker-contract wording that still described a non-canonical path. Updated the surviving runtime-facing text surfaces so workers now read one contract from the repo.",
  "whatWasImplemented": "Updated the README and contract-facing docs to reflect the frozen precedence rule between runtime code, committed docs, and persisted work-graph state; removed or clearly marked contradictory lifecycle references; and aligned any touched runtime help/report text with the same vocabulary.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {
        "command": "go test ./internal/cli ./internal/service -run 'TestReport|TestVerify|TestCheck' -count=1",
        "exitCode": 0,
        "observation": "Focused text-surface tests passed."
      },
      {
        "command": "make lint",
        "exitCode": 0,
        "observation": "go vet passed."
      },
      {
        "command": "make build",
        "exitCode": 0,
        "observation": "CLI binary built successfully."
      }
    ],
    "interactiveChecks": []
  },
  "tests": {
    "added": [
      {
        "file": "internal/cli/report_test.go",
        "cases": [
          {
            "name": "TestReportCommandUsesCanonicalEnvelope",
            "verifies": "Runtime-facing report wording stayed aligned with the canonical contract."
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- The feature exposes an unresolved disagreement between code behavior and the frozen mission contract.
- A proposed doc alignment would get ahead of runtime behavior that has not yet landed.
- The feature needs a broader runtime refactor to avoid leaving two canonical contracts alive.
