#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

BIN="${BIN:-$ROOT_DIR/bin/cagent}"
ADAPTER="${CAGENT_DOGFOOD_ADAPTER:-opencode}"
MODEL="${CAGENT_DOGFOOD_MODEL:-zai-coding-plan/glm-5}"
CWD_TARGET="${CAGENT_DOGFOOD_CWD:-$ROOT_DIR}"
TITLE="${CAGENT_DOGFOOD_TITLE:-Dogfood Web Desktop}"
OBJECTIVE="${CAGENT_DOGFOOD_OBJECTIVE:-Build and verify a tiny web desktop app using only cagent workers.}"
SKILL_PATH="$ROOT_DIR/skills/cagent/SKILL.md"
SEED_PATH="$ROOT_DIR/docs/dogfood-web-desktop-seed.md"
DOGFOOD_DIR="${CAGENT_DOGFOOD_DIR:-$ROOT_DIR/.dogfood}"
CONFIG_DIR="${CAGENT_DOGFOOD_CONFIG_DIR:-$DOGFOOD_DIR/config}"
CONFIG_PATH="${CAGENT_DOGFOOD_CONFIG:-$CONFIG_DIR/config.toml}"
STATE_DIR="${CAGENT_DOGFOOD_STATE_DIR:-$DOGFOOD_DIR/state}"
CACHE_DIR="${CAGENT_DOGFOOD_CACHE_DIR:-$DOGFOOD_DIR/cache}"
BIN_DIR="${CAGENT_DOGFOOD_BIN_DIR:-$DOGFOOD_DIR/bin}"
WRAPPER_PATH="${CAGENT_DOGFOOD_WRAPPER:-$BIN_DIR/cagent}"

if [[ ! -x "$BIN" ]]; then
  go build -o "$BIN" ./cmd/cagent
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required for bootstrap output parsing" >&2
  exit 1
fi

mkdir -p "$DOGFOOD_DIR" "$CONFIG_DIR" "$STATE_DIR" "$CACHE_DIR" "$BIN_DIR"
cat > "$CONFIG_PATH" <<EOF
[store]
state_dir = "$STATE_DIR"
EOF

cat > "$WRAPPER_PATH" <<EOF
#!/usr/bin/env bash
exec "$BIN" "\$@"
EOF
chmod +x "$WRAPPER_PATH"

export CAGENT_CONFIG_DIR="$CONFIG_DIR"
export CAGENT_STATE_DIR="$STATE_DIR"
export CAGENT_CACHE_DIR="$CACHE_DIR"
export CAGENT_EXECUTABLE="$BIN"
export PATH="$BIN_DIR:$PATH"

root_json="$("$BIN" --config "$CONFIG_PATH" --json work create \
  --title "$TITLE" \
  --objective "$OBJECTIVE" \
  --kind plan)"

root_work_id="$(printf '%s\n' "$root_json" | jq -r '.work_id')"

planner_prompt="$(cat <<EOF
You are the initial planner/coordinator for cagent work item $root_work_id.

Read these files first:
- $SEED_PATH
- $SKILL_PATH

Only read these additional docs if you are blocked on the work API shape:
- $ROOT_DIR/docs/cagent-work-runtime.md
- $ROOT_DIR/docs/cagent-work-api-and-schema.md
- $ROOT_DIR/docs/cagent-worker-briefing-schema.md

Then:
1. inspect the local runtime and model inventory with cagent
2. create the initial work graph under $root_work_id
3. publish structured work updates as you move phases
4. delegate substantive work through cagent workers only
5. prefer low-cost models by default, using stronger models sparingly
6. ensure Playwright verification emits screenshots and video artifacts when possible

Runtime scoping:
- this worker already has isolated CAGENT_* env vars set for the correct runtime
- bare "cagent" and "./bin/cagent" should both land in the same isolated runtime
- prefer bare "cagent" for runtime operations

Model routing:
- keep this root planning/coherence thread on the strongest reasoning model you already have
- use opencode/minimax-m2.5-free and opencode/mimo-v2-flash-free first for implementation
- rotate to opencode/gpt-5-nano if the faster free models rate limit
- use zai-coding-plan/glm-5 for deeper planning or verification loops that mostly wait on scripts/tests
- use claude/claude-haiku-4-5 for cheap review or synthesis when helpful
- use codex/gpt-5.4 sparingly for hard planning, recovery, or approval-critical review

Do not treat this as a one-session coding task. Treat it as work-runtime orchestration over a durable graph.
Do not do substantial implementation yourself if it should be delegated to a child work item.
Use proposals for structural graph changes, and do not self-approve implementation work.

Execute this sequence now instead of continuing to read docs:
1. cagent work update $root_work_id --phase planning --message "Inspecting isolated runtime and creating child work graph"
2. cagent runtime --json
3. cagent catalog sync --json
4. Create these child work items with cagent work create --parent $root_work_id ...
   - implement scaffold work, preferred adapters opencode
   - implement core-ui work, preferred adapters opencode
   - verify Playwright E2E work, preferred adapters opencode,claude, required capabilities browser,tool_use
   - review code review work, preferred adapters claude,codex
   - red_team adversarial/security work, preferred adapters codex,claude
   - doc release-report work, preferred adapters opencode,claude
5. Verify the graph with cagent work children $root_work_id --json
6. Publish a cagent work note-add $root_work_id --type graph --text "..."
   listing the child work ids and intended adapters/models
7. Launch exactly one first child worker for the scaffold implementation using:
   - adapter opencode
   - model opencode/minimax-m2.5-free
   - fallback models opencode/mimo-v2-flash-free, then opencode/gpt-5-nano
   - child prompt contract:
     - stay inside $ROOT_DIR and the known target path $ROOT_DIR/web-desktop
     - do not scan unrelated filesystem paths
     - do not implement core UI; leave that to the core-ui work item
     - create the scaffold, install dependencies, and verify the scaffold is buildable
     - publish:
       1. cagent work update <scaffold-work-id> --phase scaffold --message "Creating scaffold in $ROOT_DIR/web-desktop"
       2. cagent work update <scaffold-work-id> --phase scaffold --message "Scaffold created; verifying dependencies/build"
       3. cagent work note-add <scaffold-work-id> --type summary --text "Created scaffold files and scripts ..."
       4. cagent work complete <scaffold-work-id> --message "Scaffold ready for core UI implementation"
8. Leave the root work in_progress after delegation. Do not mark it done yet.

Before claiming success:
- prove the graph exists in runtime state with cagent work children $root_work_id --json
- ensure at least one child job has actually been launched
- if either condition is false, keep the root work in_progress and continue fixing the graph/runtime state
EOF
)"

run_json="$("$BIN" --config "$CONFIG_PATH" --json run \
  --adapter "$ADAPTER" \
  --model "$MODEL" \
  --cwd "$CWD_TARGET" \
  --work "$root_work_id" \
  --label "dogfood-web-desktop-planner" \
  --prompt "$planner_prompt")"

planner_job_id="$(printf '%s\n' "$run_json" | jq -r '.job.job_id')"
planner_session_id="$(printf '%s\n' "$run_json" | jq -r '.session.session_id')"

seed_artifact_json="$("$BIN" --config "$CONFIG_PATH" --json artifacts attach \
  --job "$planner_job_id" \
  --path "$SEED_PATH" \
  --kind seed)"
seed_artifact_id="$(printf '%s\n' "$seed_artifact_json" | jq -r '.artifact.artifact_id')"

cat <<EOF
config_path=$CONFIG_PATH
config_dir=$CONFIG_DIR
state_dir=$STATE_DIR
cache_dir=$CACHE_DIR
wrapper_path=$WRAPPER_PATH
root_work_id=$root_work_id
seed_artifact_id=$seed_artifact_id
planner_job_id=$planner_job_id
planner_session_id=$planner_session_id

observe with:
  ./bin/cagent --config "$CONFIG_PATH" work projection checklist $root_work_id
  ./bin/cagent --config "$CONFIG_PATH" work projection status $root_work_id
  ./bin/cagent --config "$CONFIG_PATH" work show $root_work_id
  ./bin/cagent --config "$CONFIG_PATH" logs --follow $planner_job_id
EOF
