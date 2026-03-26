---
name: go-worker
description: Go development worker for refactoring, feature implementation, and code changes
---

# Go Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

All features in this mission: rename refactoring, new Go code (channels, tools), code removal (attestation machinery), Go source modifications (state machine, MCP, email digest, skill loading), and cleanup.

## Required Skills

None.

## Work Procedure

### 1. Understand the Feature

Read the feature description, preconditions, expectedBehavior, and verificationSteps thoroughly. Then investigate the relevant code:
- Use Grep to find all files that need modification
- Read the key functions/files mentioned in the description
- Understand the current behavior before changing anything

### 2. Plan Changes

Before writing any code, list the specific files and functions you'll modify. For removal features, identify every reference to the code being removed. For rename features, find every occurrence systematically.

### 3. Write Tests First (TDD Red Phase)

Where the feature adds or changes behavior (not pure removal/rename):
- Write failing tests that assert the expected behavior
- Run `go test` to confirm they fail for the right reason
- For rename features: existing tests serve as the red-then-green mechanism (they break on rename, then pass after updating)

### 4. Implement Changes

Make the code changes. Be methodical:
- **For renames**: Use systematic find-and-replace. Process ALL files — don't leave stragglers.
- **For new code**: Write clean, idiomatic Go. Follow existing patterns in the codebase.
- **For removal**: Remove functions AND all their callers. Trace the call graph completely.
- **For modifications**: Preserve the function signatures' contracts where possible.

### 5. Make Tests Pass (TDD Green Phase)

Run tests and fix issues until green:
```
go build ./...
go list ./internal/... | grep -v codex | grep -v mcpserver | xargs env GOMAXPROCS=4 go test
go vet ./...
```

All three must exit 0. If tests fail, debug and fix. Do not skip failing tests.

### 6. Manual Verification

Run the specific verification commands from the feature's `verificationSteps`. For each step:
- Execute the command
- Record the exact output
- Confirm it matches expected behavior

Common verifications:
- `rg 'pattern' --type go` to confirm no stale references
- `cogent --help` or `cogent <subcommand> --help` for CLI checks
- Reading specific code sections to confirm structural changes

### 7. Commit

Commit with message format: `cogent(<scope>): <description>`

If the rename hasn't happened yet, use `cogent(<scope>): <description>`.

## Example Handoff

```json
{
  "salientSummary": "Removed 28 attestation-spawning functions from service.go (~750 lines). Simplified finishJob to skip spawnAttestationChildren, simplified completionGateIssues to only check for passing check record. All 3 validators pass: go build (0), go test (0, 47 tests), go vet (0).",
  "whatWasImplemented": "Removed spawnAttestationChildren and all 28 helper functions (attestationChildRuntime, attestationChildTitle, etc.). Updated finishJob to remove attestation spawning branch. Simplified completionGateIssues to only check for passing CheckRecord, removing attestation children and required attestations checks. Updated 3 test cases that referenced attestation spawning behavior.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {
        "command": "go build ./...",
        "exitCode": 0,
        "observation": "Clean build, no errors"
      },
      {
        "command": "go list ./internal/... | grep -v codex | grep -v mcpserver | xargs env GOMAXPROCS=4 go test",
        "exitCode": 0,
        "observation": "47 tests pass across store, notify, core, service packages"
      },
      {
        "command": "go vet ./...",
        "exitCode": 0,
        "observation": "No vet issues"
      },
      {
        "command": "rg 'spawnAttestationChildren|attestationChildRuntime|attestationChildTitle' --type go",
        "exitCode": 1,
        "observation": "No matches — all attestation spawning code removed"
      }
    ],
    "interactiveChecks": [
      {
        "action": "Read finishJob function to verify no attestation spawning",
        "observed": "finishJob calls syncWorkStateFromJob then returns. No branch for spawnAttestationChildren."
      },
      {
        "action": "Read completionGateIssues to verify simplified gate",
        "observed": "Only checks for passing check record via hasPassingCheck(). No attestation children or slot checks."
      }
    ]
  },
  "tests": {
    "added": [
      {
        "file": "internal/service/service_test.go",
        "cases": [
          {
            "name": "TestFinishJob_NoAttestationSpawning",
            "verifies": "Job completion does not create attestation child work items"
          }
        ]
      }
    ]
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- A function being removed is called from code outside internal/service/ that you didn't expect
- The rename reveals circular import issues that require architectural changes
- Test failures indicate a deeper issue than your feature scope covers
- Build failures in packages outside your feature's scope
- The feature depends on code that doesn't exist yet (e.g., channel tools not yet implemented)
