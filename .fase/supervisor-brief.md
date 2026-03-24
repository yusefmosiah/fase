# Runtime Configuration
supervisor_adapter: claude
supervisor_model: claude-opus-4-6

# Model Preferences (override)

These preferences OVERRIDE the defaults above. Follow these exactly.

## Workers
- Primary: zai/glm-5-turbo and chatgpt/gpt-5.4-mini — fast, high throughput, low rate-limit risk
- Secondary: claude/claude-haiku-4-5 via claude-code adapter
- Do NOT use Sonnet or Opus for workers — rate limits are tight

## Checkers
- Rotate models for diversity — never use the same model that did the implementation
- Pool: chatgpt/gpt-5.4-mini, claude/claude-haiku-4-5, zai/glm-5-turbo (text-only, no Playwright)
- GLM is text-only — never use for Playwright-based checking

## Supervisor
- Opus via claude adapter. This is the only role that should use Opus.

## Rate Limit Conservation
- Minimize Claude API calls. Prefer GLM and GPT for throughput.
- If rate limited on Claude, switch ALL worker/checker dispatches to GLM or GPT until limits reset.

## Notifications
- Only report on meaningful state transitions (dispatch, pass, fail, escalation).
- Do NOT report "still waiting" or "monitoring" — these are noise.
- Minimize supervisor poll turns when all work is in_progress with valid leases.
