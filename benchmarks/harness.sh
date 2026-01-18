#!/usr/bin/env bash
# Skill-bench harness - sets up environment and records metrics
#
# Compares 5 strategies from strategies.json:
#   bcq-full, bcq-generated, bcq-only, raw-docs, raw-guided
#
# EXECUTION MODEL:
#   This harness does NOT automatically invoke Claude Code. It:
#   1. Sets up environment (cache, logging, error injection)
#   2. Prints the task prompt file path
#   3. Waits for you to manually execute Claude Code with the appropriate skill
#   4. After completion, validates results and records metrics
#
#   Example workflow:
#     ./harness.sh --task 01 --strategy bcq-full
#     # Harness prints prompt file path
#     # You invoke Claude Code: claude /path/to/task.md --skill bcq-full
#     # Run validation: ./validate.sh check_all_todos 75
#
# Usage:
#   ./harness.sh --task 01 --strategy bcq-full
#   ./harness.sh --task all --strategy raw-docs --model haiku
#   ./harness.sh --task 07 --strategy bcq-generated --inject 429
#
# Results are written to results/<run_id>-<strategy>-<task>.json

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$BENCH_DIR/env.sh"

# Defaults
TASK=""
STRATEGY="bcq-full"
MODEL="${BCQ_BENCH_MODEL:-sonnet}"
INJECT_ERROR=""
INJECT_COUNT=1
DRY_RUN=false
TRIAL=1
AUTO_RESET=true  # Reset state before each task (prevents false positives)

# Load strategies from strategies.json
STRATEGIES_FILE="$BENCH_DIR/strategies.json"

log() { echo "[harness] $*" >&2; }
die() { echo "[harness] ERROR: $*" >&2; exit 1; }

# Parse arguments
parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --task|-t)
        TASK="$2"; shift 2 ;;
      --strategy|-s)
        STRATEGY="$2"; shift 2 ;;
      --model|-m)
        MODEL="$2"; shift 2 ;;
      --inject|-i)
        INJECT_ERROR="$2"; shift 2 ;;
      --inject-count)
        INJECT_COUNT="$2"; shift 2 ;;
      --trial)
        TRIAL="$2"; shift 2 ;;
      --dry-run)
        DRY_RUN=true; shift ;;
      --reset)
        AUTO_RESET=true; shift ;;
      --no-reset)
        AUTO_RESET=false; shift ;;
      --help|-h)
        show_help; exit 0 ;;
      *)
        die "Unknown option: $1" ;;
    esac
  done

  [[ -z "$TASK" ]] && die "Task required. Use --task <id|all>"

  # Validate strategy against strategies.json
  if ! jq -e --arg c "$STRATEGY" '.strategies | has($c)' "$STRATEGIES_FILE" >/dev/null 2>&1; then
    local valid_strategies
    valid_strategies=$(jq -r '.strategies | keys | join(", ")' "$STRATEGIES_FILE")
    die "Invalid strategy: $STRATEGY. Valid: $valid_strategies"
  fi
  :  # Explicit no-op to ensure proper return
}

show_help() {
  local strategies_list
  strategies_list=$(jq -r '.strategies | to_entries | map("  \(.key): \(.value.description)") | join("\n")' "$STRATEGIES_FILE" 2>/dev/null || echo "  (error reading strategies.json)")

  cat << EOF
Benchmark Harness - Run skill-bench tasks

Usage: $0 [options]

Options:
  --task, -t <id|all>       Task ID (01-10) or 'all'
  --strategy, -s <strategy>    Strategy name (default: bcq-full)
  --model, -m <model>       Model identifier for metadata (default: sonnet)
  --inject, -i <code>       Inject error (401 or 429) before task
  --inject-count <n>        Number of errors to inject (default: 1)
  --trial <n>               Trial number for repeated runs
  --dry-run                 Show what would run without executing
  --reset                   Reset state before each task (default: enabled)
  --no-reset                Skip state reset (use for debugging only)
  --help, -h                Show this help

Strategies (from strategies.json):
$strategies_list

Examples:
  $0 --task 01 --strategy bcq-full
  $0 --task all --strategy raw-docs --model haiku
  $0 --task 07 --strategy bcq-generated --inject 429
EOF
}

# Generate run ID and set related environment variables
generate_run_id() {
  local run_id
  run_id="$(date +%Y%m%d-%H%M%S)-${TRIAL}"

  # Export for run-specific validation (prevents false positives from prior runs)
  export BCQ_BENCH_RUN_ID="$run_id"
  export BCQ_BENCH_RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  echo "$run_id"
}

# Get strategy config from strategies.json
get_strategy_config() {
  local strategy="$1"
  local field="$2"
  jq -r --arg c "$strategy" ".strategies[\$c].$field // \"\"" "$STRATEGIES_FILE"
}

# Check if strategy uses bcq (vs raw curl)
strategy_uses_bcq() {
  local tools
  tools=$(get_strategy_config "$STRATEGY" "tools")
  echo "$tools" | jq -e 'index("bcq") != null' >/dev/null 2>&1
}

# Setup strategy-specific environment
setup_strategy() {
  log "Setting up strategy: $STRATEGY"

  # Clear cache
  rm -rf "$BCQ_CACHE_DIR"
  mkdir -p "$BCQ_CACHE_DIR"

  # Request log is now per-task (set in run_task), no global clearing needed

  # Add curl shim to PATH for request logging and error injection
  # The shim intercepts all curl calls and logs them
  export PATH="$BENCH_DIR:$PATH"

  if strategy_uses_bcq; then
    # bcq-based strategies (bcq-full, bcq-generated, bcq-only)
    # CRITICAL: Unset BASECAMP_ACCESS_TOKEN for bcq strategies
    # bcq skips token refresh when this is set, which would poison Task 08 results
    if [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]]; then
      log "WARNING: BASECAMP_ACCESS_TOKEN is set; unsetting to allow bcq token refresh"
    fi
    unset BASECAMP_ACCESS_TOKEN

    export BCQ_BENCH_USE_BCQ=true
    export BCQ_CACHE_ENABLED=true  # Real-world product behavior
  else
    # raw-based strategies (raw-docs, raw-guided)
    export BCQ_BENCH_USE_BCQ=false
    export BCQ_CACHE_ENABLED=false
  fi
}

# Setup error injection
setup_injection() {
  if [[ -n "$INJECT_ERROR" ]]; then
    log "Setting up error injection: $INJECT_ERROR x $INJECT_COUNT"
    "$BENCH_DIR/inject-proxy.sh" setup "$INJECT_ERROR" "$INJECT_COUNT"
    # curl shim is already on PATH from setup_strategy
  else
    "$BENCH_DIR/inject-proxy.sh" clear >/dev/null 2>&1 || true
  fi
}

# Get task info from spec.yaml
get_task_info() {
  local task_id="$1"
  local field="$2"

  yq -r ".tasks[] | select(.id == \"$task_id\") | .$field" "$BENCH_DIR/spec.yaml"
}

# Get injection config from spec.yaml for a task
# Sets TASK_INJECT_ERROR, TASK_INJECT_COUNT, TASK_INJECT_MATCH, TASK_INJECT_AT_REQUEST
get_task_injection() {
  local task_id="$1"

  TASK_INJECT_ERROR=$(yq -r ".tasks[] | select(.id == \"$task_id\") | .inject.error // \"\"" "$BENCH_DIR/spec.yaml")
  TASK_INJECT_COUNT=$(yq -r ".tasks[] | select(.id == \"$task_id\") | .inject.count // 1" "$BENCH_DIR/spec.yaml")
  TASK_INJECT_MATCH=$(yq -r ".tasks[] | select(.id == \"$task_id\") | .inject.match // \"\"" "$BENCH_DIR/spec.yaml")
  TASK_INJECT_AT_REQUEST=$(yq -r ".tasks[] | select(.id == \"$task_id\") | .inject.at_request // 0" "$BENCH_DIR/spec.yaml")
}

# Get skill path for strategy (from strategies.json)
get_skill_path() {
  get_strategy_config "$STRATEGY" "skill"
}

# Get skill name (basename of skill path for display)
get_skill() {
  local skill_path
  skill_path=$(get_skill_path)
  basename "$(dirname "$skill_path")"
}

# Per-task setup hooks
# Called before each task to ensure task-specific pre-conditions
setup_task() {
  local task_id="$1"

  case "$task_id" in
    09-bulk-complete-overdue)
      # Update overdue todos' due_on to yesterday (relative to NOW, not seed time)
      # This ensures they're actually overdue when the benchmark runs
      prepare_overdue_todos
      ;;
  esac
}

# Update overdue todos to have due_on = yesterday (dynamic per run)
prepare_overdue_todos() {
  local yesterday
  yesterday=$(date -v-1d +%Y-%m-%d 2>/dev/null || date -d "yesterday" +%Y-%m-%d)

  log "  Updating overdue todos: due_on=$yesterday"

  # Get overdue todo IDs
  local overdue_ids
  overdue_ids=$(bcq todos --in "$BCQ_BENCH_PROJECT_ID" --list "$BCQ_BENCH_TODOLIST_ID" --json 2>/dev/null | \
    jq -r '.data[] | select(.content | startswith("Benchmark Overdue Todo")) | .id') || true

  if [[ -z "$overdue_ids" ]]; then
    log "  WARNING: No overdue todos found to update"
    return
  fi

  # Update each via API (bcq may not support due_on update)
  local update_failures=0
  for id in $overdue_ids; do
    local http_code
    http_code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
      -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"due_on\":\"$yesterday\"}" \
      "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todos/$id.json")

    if [[ "$http_code" != "200" ]]; then
      log "  WARNING: Failed to update todo $id (HTTP $http_code)"
      ((update_failures++))
    fi

    # Also reopen if completed from previous run
    bcq reopen "$id" --project "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
  done

  if [[ "$update_failures" -gt 0 ]]; then
    log "  WARNING: $update_failures due_on updates failed (stale token?)"
  fi

  log "  Updated $(echo "$overdue_ids" | wc -w | tr -d ' ') overdue todos"
}

# Calculate prompt size (skill file + task prompt file)
# Returns JSON object with byte counts
# NOTE: This measures file sizes, not the exact prompt sent to the model.
# Claude Code may add system context that isn't captured here.
calc_prompt_size() {
  local prompt_file="$1"

  # Get skill path from strategies.json (relative to repo root)
  local skill_path
  skill_path=$(get_skill_path)
  local skill_file="$BCQ_ROOT/$skill_path"
  local task_file="$BENCH_DIR/$prompt_file"

  local skill_bytes=0 task_bytes=0 total_bytes=0

  # Follow symlinks to get actual file size
  if [[ -f "$skill_file" ]]; then
    skill_bytes=$(wc -c < "$skill_file" | tr -d ' ')
  fi

  if [[ -f "$task_file" ]]; then
    task_bytes=$(wc -c < "$task_file" | tr -d ' ')
  fi

  total_bytes=$((skill_bytes + task_bytes))

  jq -n \
    --argjson skill "$skill_bytes" \
    --argjson task "$task_bytes" \
    --argjson total "$total_bytes" \
    '{skill_bytes: $skill, task_bytes: $task, total_bytes: $total}'
}

# Run a single task
run_task() {
  local task_id="$1"
  local run_id="$2"

  # Set per-task log file to avoid cross-contamination when running --task all
  export BCQ_BENCH_LOGFILE="$BENCH_DIR/results/${run_id}-${STRATEGY}-${task_id}-requests.jsonl"
  mkdir -p "$(dirname "$BCQ_BENCH_LOGFILE")"
  rm -f "$BCQ_BENCH_LOGFILE"  # Clear per-task log

  local task_name
  task_name=$(get_task_info "$task_id" "name")
  local prompt_file
  prompt_file=$(get_task_info "$task_id" "prompt_file")
  local timeout
  timeout=$(get_task_info "$task_id" "timeout_seconds")
  local skill
  skill=$(get_skill)

  # Check for task-specific injection from spec.yaml
  # CLI --inject overrides spec.yaml if provided
  get_task_injection "$task_id"
  local effective_inject_error="${INJECT_ERROR:-$TASK_INJECT_ERROR}"
  local effective_inject_count="${INJECT_COUNT:-$TASK_INJECT_COUNT}"
  local effective_inject_match="${TASK_INJECT_MATCH:-}"
  local effective_inject_at_request="${TASK_INJECT_AT_REQUEST:-0}"

  # Setup injection for this task
  if [[ -n "$effective_inject_error" ]]; then
    local inject_opts=()
    if [[ -n "$effective_inject_match" ]]; then
      inject_opts+=(--match "$effective_inject_match")
      log "Setting up error injection: $effective_inject_error x $effective_inject_count (match: $effective_inject_match)"
    elif [[ "$effective_inject_at_request" -gt 0 ]]; then
      inject_opts+=(--at-request "$effective_inject_at_request")
      log "Setting up error injection: $effective_inject_error x $effective_inject_count (at request: $effective_inject_at_request)"
    else
      log "Setting up error injection: $effective_inject_error x $effective_inject_count"
    fi
    "$BENCH_DIR/inject-proxy.sh" setup "$effective_inject_error" "$effective_inject_count" "${inject_opts[@]}"
    # Signal to validation that injection was configured (even if it doesn't fire)
    export BCQ_BENCH_INJECTION_EXPECTED="$effective_inject_error"
  else
    "$BENCH_DIR/inject-proxy.sh" clear >/dev/null 2>&1 || true
    export BCQ_BENCH_INJECTION_EXPECTED=""
  fi

  # Calculate prompt size for this task
  local prompt_size_json
  prompt_size_json=$(calc_prompt_size "$prompt_file")
  local total_prompt_bytes
  total_prompt_bytes=$(echo "$prompt_size_json" | jq '.total_bytes')

  log "Running task $task_id: $task_name"
  log "  Strategy: $STRATEGY, Skill: $skill, Model: $MODEL"
  log "  Prompt size: $total_prompt_bytes bytes"

  if [[ "$DRY_RUN" == "true" ]]; then
    log "  [DRY RUN] Would execute: $BENCH_DIR/$prompt_file with skill $skill"
    return 0
  fi

  # Run per-task setup AFTER dry-run check (setup mutates data)
  setup_task "$task_id"

  # Create result structure
  local result_file="$BENCH_DIR/results/${run_id}-${STRATEGY}-${task_id}.json"

  # Create output file for task results (used by Task 11 validation)
  local output_file="$BENCH_DIR/results/${run_id}-${STRATEGY}-${task_id}-output.txt"
  mkdir -p "$(dirname "$output_file")"
  # Export for validation to access
  export BCQ_BENCH_OUTPUT_FILE="$output_file"

  # Write env file for subshell execution
  local env_file="$BENCH_DIR/.bench-env"
  cat > "$env_file" << ENVEOF
# Benchmark environment - source this in your shell
export PATH="$BENCH_DIR:\$PATH"
export BCQ_CACHE_DIR="$BCQ_CACHE_DIR"
export BCQ_CACHE_ENABLED="$BCQ_CACHE_ENABLED"
export BCQ_BENCH_LOGFILE="$BCQ_BENCH_LOGFILE"
export BCQ_ACCOUNT_ID="$BCQ_ACCOUNT_ID"
export BCQ_API_BASE="$BCQ_API_BASE"
export BCQ_BENCH_SEARCH_MARKER="$BCQ_BENCH_SEARCH_MARKER"
export BCQ_BENCH_OUTPUT_FILE="$output_file"
# Run-specific identifiers for validation (prevents false positives from prior runs)
export BCQ_BENCH_RUN_ID="$BCQ_BENCH_RUN_ID"
export BCQ_BENCH_RUN_START="$BCQ_BENCH_RUN_START"
ENVEOF

  # IMPORTANT: Only export BASECAMP_ACCESS_TOKEN for raw-* strategies
  # bcq skips token refresh when BASECAMP_ACCESS_TOKEN is set
  if ! strategy_uses_bcq; then
    cat >> "$env_file" << ENVEOF
# Raw strategy: provide token directly (no refresh available)
export BCQ_ACCESS_TOKEN="$BCQ_ACCESS_TOKEN"
export BASECAMP_ACCOUNT_ID="$BCQ_ACCOUNT_ID"
export BASECAMP_ACCESS_TOKEN="$BCQ_ACCESS_TOKEN"
ENVEOF
  else
    cat >> "$env_file" << ENVEOF
# bcq strategy: let bcq read from credentials and handle refresh
# CRITICAL: Unset BASECAMP_ACCESS_TOKEN to enable bcq token refresh
# (prevents stale token from raw runs poisoning Task 08)
unset BASECAMP_ACCESS_TOKEN
ENVEOF
  fi

  # Print instructions for manual execution
  log ""
  log "  ╔══════════════════════════════════════════════════════════════════╗"
  log "  ║  READY FOR MANUAL EXECUTION                                      ║"
  log "  ╠══════════════════════════════════════════════════════════════════╣"
  log "  ║  Task:  $task_id - $task_name"
  log "  ║  Skill: $skill"
  log "  ║  Prompt: $BENCH_DIR/$prompt_file"
  log "  ╠══════════════════════════════════════════════════════════════════╣"
  log "  ║  In another terminal, run:                                       ║"
  log "  ║    source $env_file"
  log "  ║    # Then execute your agent with the prompt file                ║"
  log "  ╠══════════════════════════════════════════════════════════════════╣"
  log "  ║  Press ENTER when task is complete to run validation...          ║"
  log "  ╚══════════════════════════════════════════════════════════════════╝"
  log ""

  # Record start time BEFORE waiting
  local start_ms
  start_ms=$(perl -MTime::HiRes=time -e 'printf "%.0f\n", time * 1000')

  # Wait for user to complete the task
  read -r -p "[harness] Press ENTER when task is complete... "

  local end_ms
  end_ms=$(perl -MTime::HiRes=time -e 'printf "%.0f\n", time * 1000')
  local duration_ms=$((end_ms - start_ms))

  # Count requests from log
  local request_count=0
  local error_count=0
  if [[ -f "$BCQ_BENCH_LOGFILE" ]]; then
    request_count=$(wc -l < "$BCQ_BENCH_LOGFILE" | tr -d ' ')
    error_count=$(grep -cE '"http_code":"[45][0-9]{2}"' "$BCQ_BENCH_LOGFILE" 2>/dev/null) || error_count=0
  fi

  # Write result JSON
  cat > "$result_file" << EOF
{
  "run_id": "$run_id",
  "task_id": "$task_id",
  "task_name": "$task_name",
  "strategy": "$STRATEGY",
  "model": "$MODEL",
  "trial": $TRIAL,
  "success": null,
  "metrics": {
    "time_ms": $duration_ms,
    "request_count": $request_count,
    "error_count": $error_count,
    "retry_count": 0,
    "tool_call_count": 0
  },
  "prompt_size": $prompt_size_json,
  "injection": {
    "enabled": $([ -n "$effective_inject_error" ] && echo "true" || echo "false"),
    "error": "${effective_inject_error:-null}",
    "count": ${effective_inject_count:-1}
  },
  "validation": {
    "method": "pending",
    "passed": null
  },
  "prompt_file": "$prompt_file",
  "skill": "$skill",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "log_file": "results/$(basename "$BCQ_BENCH_LOGFILE")"
}
EOF

  log "  Result written to: $result_file"
  log "  Request log: $BCQ_BENCH_LOGFILE"
  log "  Duration: ${duration_ms}ms, Requests: $request_count, Errors: $error_count"
}

# Get validation info from spec.yaml
get_validation_info() {
  local task_id="$1"
  local field="$2"

  yq -r ".tasks[] | select(.id == \"$task_id\") | .validation.$field // \"\"" "$BENCH_DIR/spec.yaml"
}

# Run validation for a task - reads from spec.yaml (canonical source)
run_validation() {
  local task_id="$1"
  local run_id="$2"

  local result_file="$BENCH_DIR/results/${run_id}-${STRATEGY}-${task_id}.json"
  if [[ ! -f "$result_file" ]]; then
    log "No result file for task $task_id, skipping validation"
    return 1
  fi

  log "Validating task $task_id..."

  # Read validation config from spec.yaml
  local custom_cmd jq_expr endpoint
  custom_cmd=$(get_validation_info "$task_id" "custom")
  jq_expr=$(get_validation_info "$task_id" "jq")
  endpoint=$(get_validation_info "$task_id" "endpoint")

  local passed=false

  if [[ -n "$custom_cmd" ]]; then
    # Custom validation - strip "validate.sh " prefix if present
    local cmd_to_run="${custom_cmd#validate.sh }"
    log "  Running custom validation: $cmd_to_run"
    # Use eval to handle quoted arguments (spec.yaml is trusted input)
    if eval "\"$BENCH_DIR/validate.sh\" $cmd_to_run"; then
      passed=true
    fi
  elif [[ -n "$jq_expr" ]] && [[ -n "$endpoint" ]]; then
    # Generic jq validation against endpoint
    log "  Running jq validation: $endpoint | $jq_expr"
    if "$BENCH_DIR/validate.sh" jq "$endpoint" "$jq_expr"; then
      passed=true
    fi
  else
    log "  No validation defined for task $task_id in spec.yaml"
    return 0
  fi

  # Update result file
  local tmp_file
  tmp_file=$(mktemp)
  jq --arg passed "$passed" '.validation.passed = ($passed == "true") | .validation.method = "neutral-curl" | .success = ($passed == "true")' "$result_file" > "$tmp_file"
  mv "$tmp_file" "$result_file"

  log "  Validation: $passed"
}

# List all task IDs
list_tasks() {
  yq -r '.tasks[].id' "$BENCH_DIR/spec.yaml"
}

# Main
main() {
  parse_args "$@"

  local run_id
  run_id=$(generate_run_id)
  # Re-export here since generate_run_id runs in a subshell
  export BCQ_BENCH_RUN_ID="$run_id"
  export BCQ_BENCH_RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "Benchmark run: $run_id"
  log "Run ID: $BCQ_BENCH_RUN_ID"
  log "Run Start: $BCQ_BENCH_RUN_START"
  log "Strategy: $STRATEGY, Model: $MODEL"
  log "Auto-reset: $AUTO_RESET"

  setup_strategy

  if [[ "$TASK" == "all" ]]; then
    for task_id in $(list_tasks); do
      # Clear cache BEFORE reset so bcq commands in reset.sh don't use stale data
      rm -rf "$BCQ_CACHE_DIR"
      mkdir -p "$BCQ_CACHE_DIR"

      # Reset before each task to ensure clean state (prevents false positives)
      if [[ "$AUTO_RESET" == "true" ]]; then
        log "Resetting state before task $task_id..."
        "$BENCH_DIR/reset.sh" 2>/dev/null || true
        # Recompute run_start AFTER reset so time-based validation works
        export BCQ_BENCH_RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        log "Run Start (post-reset): $BCQ_BENCH_RUN_START"
      fi
      setup_strategy
      # Injection is now setup per-task in run_task() from spec.yaml
      run_task "$task_id" "$run_id"
      if [[ "$DRY_RUN" != "true" ]]; then
        run_validation "$task_id" "$run_id"
      fi
    done
  else
    # Clear cache BEFORE reset so bcq commands in reset.sh don't use stale data
    rm -rf "$BCQ_CACHE_DIR"
    mkdir -p "$BCQ_CACHE_DIR"

    # Reset before single task too
    if [[ "$AUTO_RESET" == "true" ]]; then
      log "Resetting state before task..."
      "$BENCH_DIR/reset.sh" 2>/dev/null || true
      # Recompute run_start AFTER reset so time-based validation works
      export BCQ_BENCH_RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      log "Run Start (post-reset): $BCQ_BENCH_RUN_START"
    fi
    run_task "$TASK" "$run_id"
    if [[ "$DRY_RUN" != "true" ]]; then
      run_validation "$TASK" "$run_id"
    fi
  fi

  # Clean up injection
  "$BENCH_DIR/inject-proxy.sh" clear >/dev/null 2>&1 || true

  log "Run complete: $run_id"

  # Summary
  if [[ "$TASK" == "all" ]]; then
    log "Results:"
    for f in "$BENCH_DIR/results/${run_id}-${STRATEGY}-"*.json; do
      [[ -f "$f" ]] || continue
      local tid success
      tid=$(jq -r '.task_id' "$f")
      success=$(jq -r '.success // "pending"' "$f")
      echo "  Task $tid: $success"
    done
  fi
}

main "$@"
