#!/usr/bin/env bash
# Benchmark harness - model-agnostic agent runner with full instrumentation
#
# Requires bash 4.0+ for associative arrays

# Enforce bash version
if ((BASH_VERSINFO[0] < 4)); then
  echo "Error: bash 4.0+ required (found ${BASH_VERSION})" >&2
  echo "On macOS: brew install bash && /opt/homebrew/bin/bash $0 $*" >&2
  exit 1
fi
#
# Usage:
#   ./harness/run.sh --task 12 --strategy bcq --model claude-sonnet
#   ./harness/run.sh --task 12 --strategy api-guided --model gpt-4o
#   ./harness/run.sh --task 12 --strategy bcq --model claude-haiku --thinking
#
# Supported models:
#   Anthropic: claude-sonnet, claude-haiku, claude-opus
#   OpenAI:    gpt-4o, gpt-4-turbo, o1-mini
#
# Environment variables:
#   ANTHROPIC_API_KEY - Required for Claude models
#   OPENAI_API_KEY    - Required for GPT models
#   LOG_LEVEL         - debug, info, warn, error (default: info)

set -euo pipefail

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(dirname "$HARNESS_DIR")"

# Source library modules
source "$HARNESS_DIR/lib/logging.sh"
source "$HARNESS_DIR/lib/core.sh"

# === Strategy Registry ===
STRATEGIES_FILE="$BENCH_DIR/strategies.json"

# Get list of all strategy names from strategies.json
get_strategy_list() {
  if [[ -f "$STRATEGIES_FILE" ]]; then
    jq -r '.strategies | keys[]' "$STRATEGIES_FILE" 2>/dev/null | sort | tr '\n' ',' | sed 's/,$//'
  else
    echo "bcq-full,api-guided"  # fallback
  fi
}

# Get strategy config (prompt or skill path) from strategies.json
get_strategy_config() {
  local strategy="$1"
  local field="$2"  # "prompt" or "skill"
  if [[ -f "$STRATEGIES_FILE" ]]; then
    jq -r --arg s "$strategy" --arg f "$field" '.strategies[$s][$f] // empty' "$STRATEGIES_FILE" 2>/dev/null
  fi
}

# Validate strategy exists in registry
validate_strategy() {
  local strategy="$1"
  if [[ -f "$STRATEGIES_FILE" ]]; then
    jq -e --arg s "$strategy" '.strategies[$s]' "$STRATEGIES_FILE" >/dev/null 2>&1
  else
    return 0  # Allow anything if no registry
  fi
}

# === Configuration ===
TASK=""
STRATEGY=""
MODEL=""
MAX_TURNS=50
TIMEOUT=300
DRY_RUN=false

# Track resolved prompt path for metrics
RESOLVED_PROMPT_PATH=""

# === Argument Parsing ===
usage() {
  local strategies
  strategies=$(get_strategy_list)
  cat <<EOF
Usage: $(basename "$0") [options]

Options:
  --task, -t <id>        Task ID (e.g., 12)
  --strategy, -s <name>  Strategy: ${strategies}
  --model, -m <name>     Model: claude-sonnet, claude-haiku, gpt-4o, etc.
  --max-turns <n>        Maximum agent turns (default: 50)
  --timeout <seconds>    Execution timeout (default: 300)
  --thinking             Enable extended thinking (opus only)
  --dry-run              Validate config and resolve prompts without running agent
  --help, -h             Show this help

Examples:
  $(basename "$0") --task 12 --strategy bcq-full --model claude-sonnet
  $(basename "$0") --task 12 --strategy api-guided --model gpt-4o
  $(basename "$0") --task 12 --strategy bcq-only --model claude-haiku --dry-run
EOF
  exit 1
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --task|-t)
        TASK="$2"
        shift 2
        ;;
      --strategy|-s)
        STRATEGY="$2"
        shift 2
        ;;
      --model|-m)
        MODEL="$2"
        shift 2
        ;;
      --max-turns)
        MAX_TURNS="$2"
        shift 2
        ;;
      --timeout)
        TIMEOUT="$2"
        shift 2
        ;;
      --thinking)
        export ANTHROPIC_THINKING=true
        shift
        ;;
      --dry-run)
        DRY_RUN=true
        shift
        ;;
      --help|-h)
        usage
        ;;
      *)
        log_error "Unknown option: $1"
        usage
        ;;
    esac
  done

  if [[ -z "$TASK" ]]; then log_error "Missing required: --task"; usage; fi
  if [[ -z "$STRATEGY" ]]; then log_error "Missing required: --strategy"; usage; fi
  if [[ -z "$MODEL" ]]; then log_error "Missing required: --model"; usage; fi
}

# === Preflight Checks ===
# Validates environment before spending API tokens on a run that will fail
preflight_check() {
  local errors=()

  # Source env.sh to get environment variables
  if [[ -f "$BENCH_DIR/env.sh" ]]; then
    source "$BENCH_DIR/env.sh" 2>/dev/null || true
  fi

  # Check required Basecamp environment
  [[ -z "${BASECAMP_TOKEN:-}" ]] && errors+=("BASECAMP_TOKEN not set")
  [[ -z "${BCQ_ACCOUNT_ID:-}" ]] && errors+=("BCQ_ACCOUNT_ID not set")

  # Run ID will be generated if not set, but fixture IDs are required
  [[ -z "${BCQ_BENCH_PROJECT_ID:-}" ]] && errors+=("BCQ_BENCH_PROJECT_ID not set - run seed.sh?")

  # Task-specific fixture requirements
  case "$TASK" in
    12|12-overdue-sweep)
      # Task 12 requires both benchmark projects
      [[ -z "${BCQ_BENCH_PROJECT_ID_2:-}" ]] && errors+=("BCQ_BENCH_PROJECT_ID_2 not set - required for task 12")
      [[ -z "${BCQ_BENCH_TODOSET_ID:-}" ]] && errors+=("BCQ_BENCH_TODOSET_ID not set - required for task 12")
      [[ -z "${BCQ_BENCH_TODOSET_ID_2:-}" ]] && errors+=("BCQ_BENCH_TODOSET_ID_2 not set - required for task 12")
      ;;
  esac

  # Check model API key availability (skip in dry-run mode)
  if [[ "$DRY_RUN" != "true" ]]; then
    case "$MODEL" in
      claude-*|anthropic/*)
        [[ -z "${ANTHROPIC_API_KEY:-}" ]] && errors+=("ANTHROPIC_API_KEY not set (required for $MODEL)")
        ;;
      gpt-*|openai/*|o1*|o3*)
        [[ -z "${OPENAI_API_KEY:-}" ]] && errors+=("OPENAI_API_KEY not set (required for $MODEL)")
        ;;
    esac
  fi

  # Check task file exists
  local task_file="$BENCH_DIR/tasks/${TASK}-"*.md
  if ! compgen -G "$task_file" > /dev/null 2>&1; then
    if [[ ! -d "$BENCH_DIR/tasks/$TASK" ]]; then
      errors+=("Task file not found: tasks/${TASK}-*.md or tasks/$TASK/")
    fi
  fi

  # Injection sanity: if injection configured in spec.yaml, verify match pattern
  local spec_file="$BENCH_DIR/spec.yaml"
  if [[ -f "$spec_file" ]]; then
    local inject_error inject_match
    inject_error=$(yq -r ".tasks[] | select(.id == \"$TASK\") | .inject.error // \"\"" "$spec_file" 2>/dev/null || echo "")
    inject_match=$(yq -r ".tasks[] | select(.id == \"$TASK\") | .inject.match // \"\"" "$spec_file" 2>/dev/null || echo "")
    if [[ -n "$inject_error" ]] && [[ -z "$inject_match" ]]; then
      errors+=("Injection error=$inject_error configured but no match pattern")
    fi
  fi

  # Report all errors
  if [[ ${#errors[@]} -gt 0 ]]; then
    echo "Preflight check failed:" >&2
    printf '  - %s\n' "${errors[@]}" >&2
    return 1
  fi

  return 0
}

# === Model Loading ===
load_model_adapter() {
  local model="$1"

  case "$model" in
    claude-*|anthropic/*)
      source "$HARNESS_DIR/lib/models/anthropic.sh"
      ;;
    gpt-*|openai/*|o1*|o3*)
      source "$HARNESS_DIR/lib/models/openai.sh"
      ;;
    *)
      log_error "Unknown model family: $model"
      log_error "Supported: claude-*, gpt-*, o1*, o3*"
      exit 1
      ;;
  esac

  model_init "$model"
}

# === Task Loading ===
# Resolves and loads the prompt for a task+strategy combination.
# Combines: task-specific instructions + strategy documentation
# Sets RESOLVED_PROMPT_PATH global for metrics tracking.
# Fails fast if required files don't exist.
load_task() {
  local task_id="$1"
  local strategy="$2"

  local task_dir="$BENCH_DIR/tasks/$task_id"
  local task_prompt="" strategy_docs=""

  # === Step 1: Load task-specific instructions ===
  # Determine which task prompt variant to use based on strategy type
  local task_prompt_file=""
  if [[ "$strategy" == bcq* ]]; then
    task_prompt_file="$task_dir/prompt-bcq.md"
  elif [[ "$strategy" == api* ]]; then
    task_prompt_file="$task_dir/prompt-raw.md"
  fi

  # Try strategy-specific prompt first, then type-based, then generic
  if [[ -f "$task_dir/prompt-${strategy}.md" ]]; then
    task_prompt_file="$task_dir/prompt-${strategy}.md"
  fi

  if [[ -n "$task_prompt_file" ]] && [[ -f "$task_prompt_file" ]]; then
    task_prompt=$(cat "$task_prompt_file")
    RESOLVED_PROMPT_PATH="tasks/$task_id/$(basename "$task_prompt_file")"
  else
    # Fall back to task.yaml or generic task file
    local task_file
    task_file=$(compgen -G "$BENCH_DIR/tasks/${task_id}-"*.md | head -1)
    if [[ -n "$task_file" ]] && [[ -f "$task_file" ]]; then
      task_prompt=$(cat "$task_file")
      RESOLVED_PROMPT_PATH="tasks/$(basename "$task_file")"
    fi
  fi

  if [[ -z "$task_prompt" ]]; then
    log_error "No task instructions found for task=$task_id strategy=$strategy"
    log_error "  Checked: prompt-${strategy}.md, prompt-bcq.md, prompt-raw.md, ${task_id}-*.md"
    exit 1
  fi

  # === Step 2: Load strategy documentation (optional context) ===
  local strategy_prompt strategy_skill
  strategy_prompt=$(get_strategy_config "$strategy" "prompt")
  strategy_skill=$(get_strategy_config "$strategy" "skill")

  if [[ -n "$strategy_prompt" ]]; then
    local prompt_path="$BENCH_DIR/../$strategy_prompt"
    if [[ -f "$prompt_path" ]]; then
      strategy_docs=$(cat "$prompt_path")
    else
      log_error "Strategy '$strategy' specifies prompt '$strategy_prompt' but file not found: $prompt_path"
      exit 1
    fi
  elif [[ -n "$strategy_skill" ]]; then
    local skill_path="$BENCH_DIR/../$strategy_skill"
    if [[ -f "$skill_path" ]]; then
      strategy_docs=$(cat "$skill_path")
    else
      log_error "Strategy '$strategy' specifies skill '$strategy_skill' but file not found: $skill_path"
      exit 1
    fi
  fi

  # === Step 3: Combine task instructions + strategy docs ===
  if [[ -n "$strategy_docs" ]]; then
    echo "$task_prompt"
    echo ""
    echo "---"
    echo ""
    echo "## Reference: Strategy Documentation"
    echo ""
    echo "$strategy_docs"
  else
    echo "$task_prompt"
  fi
}

# === System Prompt ===
build_system_prompt() {
  local strategy="$1"

  cat <<EOF
You are a benchmark agent executing tasks against a Basecamp API.

## Environment
- Working directory: $BENCH_DIR
- Source env.sh before any bcq or API commands: \`source env.sh\`
- bcq CLI: $BENCH_DIR/../bin/bcq
- Use /opt/homebrew/bin/bash for bash commands requiring bash 4+

## Available Environment Variables (after sourcing env.sh)
- BCQ_ACCOUNT_ID: Basecamp account ID
- BCQ_BENCH_PROJECT_ID: Benchmark project 1
- BCQ_BENCH_PROJECT_ID_2: Benchmark project 2

## Instructions
Execute the task described in the user message. Be efficient with API calls.
Report what you did and the results when complete.

## Strategy: $strategy
$(if [[ "$strategy" == bcq* ]]; then
  echo "Use the bcq CLI tool for all operations."
else
  echo "Use raw curl commands only. No bcq CLI."
fi)
EOF
}

# === Error Injection ===
setup_injection() {
  local task_id="$1"
  local spec_file="$BENCH_DIR/spec.yaml"

  if [[ ! -f "$spec_file" ]]; then
    log_debug "No spec.yaml found, skipping injection setup"
    return
  fi

  # Extract injection config for this task from spec.yaml
  local inject_config
  inject_config=$(yq -o=json ".tasks[] | select(.id == \"$task_id\") | .inject" "$spec_file" 2>/dev/null || echo "")

  if [[ -z "$inject_config" ]] || [[ "$inject_config" == "null" ]]; then
    # Clear any stale injection state
    rm -f "$BENCH_DIR/.inject-state"
    export BCQ_BENCH_INJECTION_EXPECTED=""
    export BCQ_INJECT_MATCH=""
    log_debug "No injection config for task $task_id"
    return
  fi

  # Parse injection parameters
  local error count match
  error=$(echo "$inject_config" | jq -r '.error // ""')
  count=$(echo "$inject_config" | jq -r '.count // 1')
  match=$(echo "$inject_config" | jq -r '.match // ""')

  if [[ -n "$error" ]]; then
    # Write injection state file: "error count triggered match"
    # Format: <error_code> <remaining_count> <triggered_count> [match_pattern]
    echo "$error $count 0 $match" > "$BENCH_DIR/.inject-state"
    # Signal to validation that injection was configured (even if it doesn't fire)
    export BCQ_BENCH_INJECTION_EXPECTED="$error"
    export BCQ_INJECT_MATCH="$match"
    log_info "Error injection: $error (count=$count, match='$match')"
  else
    export BCQ_BENCH_INJECTION_EXPECTED=""
  fi
}

# === Validation ===
run_validation() {
  local task_id="$1"

  log_info "Running validation..."

  local validate_script="$BENCH_DIR/tasks/$task_id/validate.sh"
  if [[ ! -x "$validate_script" ]]; then
    validate_script="$BENCH_DIR/validate.sh"
  fi

  if [[ -x "$validate_script" ]]; then
    # Run validation for the specific task
    case "$task_id" in
      12|12-overdue-sweep)
        "$BENCH_DIR/validate.sh" check_overdue_chain 2>&1 && return 0 || return 1
        ;;
      *)
        "$validate_script" 2>&1 && return 0 || return 1
        ;;
    esac
  fi

  log_warn "No validation script found"
  return 0
}

# === Main ===
main() {
  parse_args "$@"

  # Preflight checks - fail fast before creating log dirs or spending API tokens
  if ! preflight_check; then
    exit 1
  fi

  # Load task prompt early to validate prompt resolution
  # This must happen before dry-run exit so we catch missing prompts
  log_info "Loading task: $TASK ($STRATEGY)"
  local task_prompt
  task_prompt=$(load_task "$TASK" "$STRATEGY")

  # Build system prompt
  local system_prompt
  system_prompt=$(build_system_prompt "$STRATEGY")

  # Compute prompt hash for cohort integrity tracking
  local prompt_hash
  prompt_hash=$(echo -n "${system_prompt}${task_prompt}" | shasum -a 256 | cut -d' ' -f1 | cut -c1-12)

  # Dry-run mode: validate and exit without running agent
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "=== Dry Run: Configuration Valid ==="
    echo "Task:          $TASK"
    echo "Strategy:      $STRATEGY"
    echo "Model:         $MODEL"
    echo "Prompt path:   $RESOLVED_PROMPT_PATH"
    echo "Prompt hash:   $prompt_hash"
    echo "Prompt length: $(echo "$task_prompt" | wc -c | tr -d ' ') bytes"
    echo ""
    echo "System prompt preview:"
    echo "$system_prompt" | head -10
    echo "..."
    echo ""
    echo "Task prompt preview:"
    echo "$task_prompt" | head -10
    echo "..."
    exit 0
  fi

  # Initialize logging
  init_logging "$TASK" "$STRATEGY" "$MODEL" "$BENCH_DIR/results"

  # Export resolved prompt path for metrics
  export BCQ_BENCH_PROMPT_PATH="$RESOLVED_PROMPT_PATH"
  export BCQ_BENCH_PROMPT_HASH="$prompt_hash"
  log_debug "Prompt path: $RESOLVED_PROMPT_PATH"
  log_debug "Prompt hash: $prompt_hash"

  # Load model adapter
  load_model_adapter "$MODEL"

  # Set up environment
  log_info "Setting up environment..."
  cd "$BENCH_DIR"
  source "$BENCH_DIR/env.sh"
  export PATH="/opt/homebrew/bin:$BENCH_DIR/../bin:$BENCH_DIR:$PATH"

  # Set TOOL_FILTER based on strategy to enforce bcq vs api
  # Filter is a regex matched against the command - blocks if NO match
  # Common plumbing commands allowed in both: source, cd, date, sleep, echo, printf, test ([), true, false
  local PLUMBING='^(source |cd |\. |date|sleep|echo|printf|cat |head |tail |wc |test |\[|true|false|mkdir |rm |export )'

  case "$STRATEGY" in
    bcq*)
      # Allow bcq CLI + plumbing - block curl to API
      export TOOL_FILTER="$PLUMBING|bcq |/opt/homebrew/bin/bash|bash "
      log_info "Tool filter: bcq + plumbing (no raw curl)"
      ;;
    api*)
      # Allow curl/jq + plumbing - block bcq
      export TOOL_FILTER="$PLUMBING|curl |jq |grep |sed |awk |sort |uniq |cut |tr "
      log_info "Tool filter: curl/jq + plumbing (no bcq)"
      ;;
    *)
      log_warn "Unknown strategy '$STRATEGY', no tool filter applied"
      export TOOL_FILTER=""
      ;;
  esac

  # Reset fixtures for tasks that modify state (task 12: overdue sweep)
  if [[ "$TASK" == "12" ]]; then
    log_info "Resetting fixtures for task $TASK..."
    "$BENCH_DIR/reset.sh" >/dev/null 2>&1 || log_warn "Reset failed (continuing anyway)"
  fi

  # Set up error injection from spec.yaml if task has injection config
  setup_injection "$TASK"

  # Export max turns for triage consistency
  export BCQ_BENCH_MAX_TURNS="$MAX_TURNS"

  # Run agent (capture errors separately)
  log_info "Starting agent..."
  local agent_output agent_errors
  agent_errors=""
  {
    agent_output=$(run_agent "$system_prompt" "$task_prompt" "$MAX_TURNS" 2>&1)
  } || {
    agent_errors="Agent exited with non-zero status"
  }

  # Capture any error patterns from agent output
  if echo "$agent_output" | grep -qiE "error|failed|exception|traceback"; then
    agent_errors="${agent_errors}$(echo "$agent_output" | grep -iE "error|failed|exception|traceback" | head -20)"
  fi

  # Write errors.txt if any errors captured
  if [[ -n "$agent_errors" ]]; then
    printf "%s\n" "$agent_errors" > "$LOG_DIR/errors.txt"
  fi

  # Load metrics from subshell (run_agent saves them to a temp file)
  load_metrics

  log_info "Agent output: ${agent_output:0:200}..."

  # Run validation
  local success=false
  local validation_output=""
  local validation_status=1
  validation_output=$(run_validation "$TASK" 2>&1) && validation_status=0
  printf "%s\n" "$validation_output" > "$LOG_DIR/validation.txt"
  printf "%s\n" "$validation_output" >&2
  if [[ "$validation_status" -eq 0 ]]; then
    success=true
    log_info "Validation: PASS"
  else
    log_warn "Validation: FAIL"
  fi

  # Finalize and output results
  finalize_results "$success"
}

main "$@"
