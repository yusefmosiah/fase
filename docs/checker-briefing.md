# Checker Briefing

This reference doc mirrors the generated checker prompt in `internal/service/service.go`.
Checkers exist to collect evidence, verify deliverables, and submit a structured check record.

## Required Verification

1. Read the work objective and `git diff main...HEAD --stat`.
2. Verify any file paths named in the objective actually exist on disk with `test -e <path>`.
3. Run `go build ./...`.
4. Run targeted tests for the files or behavior touched by the diff.
5. If the work is UI-tagged or touches browser-facing paths such as `mind-graph/`, `index.html`, `playwright.config.*`, `.tsx`, `.jsx`, `.css`, or `.html`, run Playwright with a strong multimodal model (Claude Sonnet/Opus).
6. Persist Playwright screenshots and videos to `.fase/artifacts/<work-id>/screenshots/`, verify those files exist before passing the check, and fail on broken filters, duplicate sections, or fallback/placeholder data.
7. Include the commands run, verified paths, and evidence locations in `checker_notes` or `test_output`.

## Check Record Submission

Use `fase check create` or the `check_record_create` MCP tool. The CLI path should include screenshot and video evidence when UI work is checked:

```bash
fase check create <work-id> \
  --result pass|fail \
  --build-ok \
  --tests-passed <N> \
  --tests-failed <N> \
  --test-output "<commands and important output>" \
  --diff-stat "$(git diff --stat main...HEAD)" \
  --screenshots "/abs/path/one.png,/abs/path/two.png" \
  --videos "/abs/path/run.webm" \
  --notes "What you verified, what failed, and where evidence was saved"
```

## Passing Rules

- If `go build ./...` fails, the check must fail.
- If targeted tests fail, the check must fail.
- If the objective names files that are missing, the check must fail.
- If the work is UI-related and persisted screenshots are missing, the check must fail.
- A passing check record should only be submitted with `build_ok=true` and real artifact paths that exist on disk.

## Guardrails

- Do not modify code.
- Do not create new work items.
- Do not call `fase work attest`.
- Do not call `fase work update` from the checker path.
