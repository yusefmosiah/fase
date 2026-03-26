# Control Plane Worker

General-purpose worker for Go runtime and control-plane features in the Cogent codebase.

## Procedure

1. Read the feature description, preconditions, expectedBehavior, and verificationSteps carefully.
2. Read `AGENTS.md` for mission boundaries and coding conventions.
3. Read `.factory/library/contracts.md` and `.factory/library/architecture.md` for domain context.
4. Write characterization or regression tests before making behavior changes when practical.
5. Implement the feature, matching existing code style and patterns.
6. When eliminating deprecated states or constants:
   - Search the **entire codebase** for references to the deprecated symbol.
   - Address every write path (replace with canonical) and every read path (normalize or leave Canonical() infrastructure).
   - Do not consider the feature done until `rg` for the deprecated constant in write paths returns zero matches.
7. Run all verification steps listed in the feature.
8. Verify that deprecated/duplicate paths covered by the feature are actually removed or exact aliases, not just documented or partially addressed.
9. Commit with a descriptive message referencing the feature ID.

## Handoff

Return a structured handoff with:
- What was implemented (list concrete changes)
- What was left undone (if anything)
- Verification results (commands run, exit codes, observations)
- Tests added (file, case names, what each verifies)
- Discovered issues (blocking or suggestions for the orchestrator)
