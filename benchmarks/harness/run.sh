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
#   ./harness/run.sh --task 12 --condition bcq --model claude-sonnet
#   ./harness/run.sh --task 12 --condition raw --model gpt-4o
#   ./harness/run.sh --task 12 --condition bcq --model claude-haiku --thinking
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

# === Configuration ===
TASK=""
CONDITION=""
MODEL=""
MAX_TURNS=50
TIMEOUT=300

# === Argument Parsing ===
usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Options:
  --task, -t <id>        Task ID (e.g., 12)
  --condition, -c <name> Condition: bcq or raw
  --model, -m <name>     Model: claude-sonnet, claude-haiku, gpt-4o, etc.
  --max-turns <n>        Maximum agent turns (default: 50)
  --timeout <seconds>    Execution timeout (default: 300)
  --thinking             Enable extended thinking (opus only)
  --help, -h             Show this help

Examples:
  $(basename "$0") --task 12 --condition bcq --model claude-sonnet
  $(basename "$0") --task 12 --condition raw --model gpt-4o
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
      --condition|-c)
        CONDITION="$2"
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
  if [[ -z "$CONDITION" ]]; then log_error "Missing required: --condition"; usage; fi
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

  # Check model API key availability
  case "$MODEL" in
    claude-*|anthropic/*)
      [[ -z "${ANTHROPIC_API_KEY:-}" ]] && errors+=("ANTHROPIC_API_KEY not set (required for $MODEL)")
      ;;
    gpt-*|openai/*|o1*|o3*)
      [[ -z "${OPENAI_API_KEY:-}" ]] && errors+=("OPENAI_API_KEY not set (required for $MODEL)")
      ;;
  esac

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
load_task() {
  local task_id="$1"
  local condition="$2"

  local task_dir="$BENCH_DIR/tasks/$task_id"

  # Check for task.yaml or fall back to markdown prompts
  if [[ -f "$task_dir/task.yaml" ]]; then
    local task_def
    task_def=$(yq -o=json '.' "$task_dir/task.yaml")

    # Get prompt file for condition
    local prompt_file
    prompt_file=$(echo "$task_def" | jq -r --arg c "$condition" \
      '.conditions[] | select(.id == $c) | .prompt_file // empty')

    if [[ -n "$prompt_file" ]] && [[ -f "$task_dir/$prompt_file" ]]; then
      cat "$task_dir/$prompt_file"
      return
    fi
  fi

  # Fall back to direct markdown files
  local prompt_file="$task_dir/prompt-${condition}.md"
  if [[ -f "$prompt_file" ]]; then
    cat "$prompt_file"
    return
  fi

  # Fall back to task markdown file
  local task_file="$BENCH_DIR/tasks/${task_id}-"*.md
  if compgen -G "$task_file" > /dev/null; then
    cat $task_file
    return
  fi

  log_error "No prompt found for task=$task_id condition=$condition"
  exit 1
}

# === System Prompt ===
build_system_prompt() {
  local condition="$1"

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

## Condition: $condition
$(if [[ "$condition" == "bcq" ]]; then
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

  # Initialize logging
  init_logging "$TASK" "$CONDITION" "$MODEL" "$BENCH_DIR/results"

  # Load model adapter
  load_model_adapter "$MODEL"

  # Set up environment
  log_info "Setting up environment..."
  cd "$BENCH_DIR"
  source "$BENCH_DIR/env.sh"
  export PATH="/opt/homebrew/bin:$BENCH_DIR/../bin:$BENCH_DIR:$PATH"

  # Set TOOL_FILTER based on condition to enforce bcq vs raw
  # Filter is a regex matched against the command - blocks if NO match
  # Common plumbing commands allowed in both: source, cd, date, sleep, echo, printf, test ([), true, false
  local PLUMBING='^(source |cd |\. |date|sleep|echo|printf|cat |head |tail |wc |test |\[|true|false|mkdir |rm |export )'

  case "$CONDITION" in
    bcq*)
      # Allow bcq CLI + plumbing - block curl to API
      export TOOL_FILTER="$PLUMBING|bcq |/opt/homebrew/bin/bash|bash "
      log_info "Tool filter: bcq + plumbing (no raw curl)"
      ;;
    raw*)
      # Allow curl/jq + plumbing - block bcq
      export TOOL_FILTER="$PLUMBING|curl |jq |grep |sed |awk |sort |uniq |cut |tr "
      log_info "Tool filter: curl/jq + plumbing (no bcq)"
      ;;
    *)
      log_warn "Unknown condition '$CONDITION', no tool filter applied"
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

  # Load task prompt
  log_info "Loading task: $TASK ($CONDITION)"
  local task_prompt
  task_prompt=$(load_task "$TASK" "$CONDITION")

  # Build system prompt
  local system_prompt
  system_prompt=$(build_system_prompt "$CONDITION")

  # Compute prompt hash for cohort integrity tracking
  local prompt_hash
  prompt_hash=$(echo -n "${system_prompt}${task_prompt}" | shasum -a 256 | cut -d' ' -f1 | cut -c1-12)
  export BCQ_BENCH_PROMPT_HASH="$prompt_hash"
  log_debug "Prompt hash: $prompt_hash"

  # Export max turns for triage consistency
  export BCQ_BENCH_MAX_TURNS="$MAX_TURNS"

  # Run agent
  log_info "Starting agent..."
  local agent_output
  agent_output=$(run_agent "$system_prompt" "$task_prompt" "$MAX_TURNS")

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
