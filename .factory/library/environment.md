# Environment

Environment variables, external dependencies, and setup notes.

**What belongs here:** Required env vars, external API keys/services, dependency quirks, platform-specific notes.
**What does NOT belong here:** Service ports/commands (use `.factory/services.yaml`).

---

## Required Environment Variables

- `RESEND_API_KEY` — API key for Resend email service (optional: only needed for email notifications)
- `EMAIL_TO` — Recipient email address for notifications (optional: only needed for email)

## Go Version

Go 1.25.0 required (specified in go.mod).

## Build Dependencies

All Go dependencies managed via go.mod. `go mod download` fetches them.

## Platform Notes

- SQLite is embedded (no external DB needed)
- Binary installs to `~/.local/bin/` (macOS/Linux)
- State directory: `~/.cogent/` or project-local `.cogent/`
