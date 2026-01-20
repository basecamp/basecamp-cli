#!/usr/bin/env bash
# benchmarks/harness/triage.sh
# Classifies run failures and determines rerun eligibility
#
# Usage:
#   ./harness/triage.sh <run_dir>              # Print classification
#   ./harness/triage.sh --update <run_dir>     # Update metrics.json in place
#
# Output format: CLASS:detail
#   VALID              - Run completed, model had fair chance
#   HARNESS_BUG        - Our fault: wrong prompt, missing env, injection didn't fire
#   INFRA_FLAKE        - Transient: 5xx storms, auth expired, network failures
#   MODEL_FAILURE      - Model's fault: no tool use, wrong syntax, incomplete
#   DATA_ISSUE         - Fixture problem: missing data, wrong IDs
#   API_UNAVAILABLE    - Model API returned error, 0 tokens

set -euo pipefail

UPDATE_MODE=false
RUN_DIR=""

# Parse arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --update)
      UPDATE_MODE=true
      shift
      ;;
    *)
      RUN_DIR="$1"
      shift
      ;;
  esac
done

if [[ -z "$RUN_DIR" ]]; then
  echo "Usage: $0 [--update] <run_dir>" >&2
  exit 1
fi

# Locate files
metrics="$RUN_DIR/metrics.json"
tools="$RUN_DIR/tools.jsonl"
http="$RUN_DIR/http.jsonl"
validation="$RUN_DIR/validation.txt"

if [[ ! -f "$metrics" ]]; then
  echo "ERROR:metrics_not_found:true"
  exit 1
fi

# === Classification Logic ===

triage_run() {
  # Already passed? Valid success.
  if jq -e '.success == true' "$metrics" >/dev/null 2>&1; then
    echo "VALID:success"
    return 0
  fi

  # Check API_UNAVAILABLE first (most obvious)
  local tokens turns tool_calls
  tokens=$(jq -r '.metrics.tokens.total // 0' "$metrics")
  turns=$(jq -r '.metrics.turns // 0' "$metrics")
  tool_calls=$(jq -r '.metrics.tools.calls // 0' "$metrics")

  if [[ "$tokens" -eq 0 ]] || { [[ "$turns" -le 1 ]] && [[ "$tool_calls" -eq 0 ]]; }; then
    echo "API_UNAVAILABLE:zero_tokens_or_no_tools"
    return 1
  fi

  # Check HARNESS_BUG patterns
  if [[ -f "$tools" ]]; then
    # Environment not set
    if grep -qi "not set\|command not found\|env: " "$tools" 2>/dev/null; then
      echo "HARNESS_BUG:env_or_command_missing"
      return 1
    fi

    # Quote escaping bug (single quotes preventing variable expansion)
    if grep -q "'\$BCQ_BENCH\|'\$BASECAMP" "$tools" 2>/dev/null; then
      echo "HARNESS_BUG:single_quote_no_expansion"
      return 1
    fi
  fi

  # Check for validation file containing harness-level errors
  if [[ -f "$validation" ]]; then
    if grep -qi "BCQ_BENCH_.*not set\|BASECAMP_TOKEN.*not set" "$validation" 2>/dev/null; then
      echo "HARNESS_BUG:env_not_set_in_validation"
      return 1
    fi
  fi

  # Check injection didn't fire when configured
  local injection_expected
  injection_expected=$(jq -r '.injection.expected // ""' "$metrics" 2>/dev/null || echo "")
  if [[ -n "$injection_expected" ]] && [[ -f "$http" ]]; then
    # Check if expected error code appeared
    if ! grep -q "\"http_code\":\"$injection_expected\"" "$http" 2>/dev/null; then
      # Check if the triggering request even happened (e.g., page=2 for pagination)
      local inject_match
      inject_match=$(jq -r '.injection.match // ""' "$metrics" 2>/dev/null || echo "")
      if [[ -n "$inject_match" ]] && ! grep -q "$inject_match" "$http" 2>/dev/null; then
        echo "HARNESS_BUG:injection_not_triggered_request_not_made"
        return 1
      fi
    fi
  fi

  # Check INFRA_FLAKE patterns (real 5xx, not injected)
  if [[ -f "$http" ]]; then
    local total_5xx injected_5xx
    total_5xx=$(grep -cE '"http_code":"5[0-9]{2}"' "$http" 2>/dev/null) || total_5xx=0
    injected_5xx=$(grep -c '"injected":true.*"http_code":"5' "$http" 2>/dev/null) || injected_5xx=0
    if [[ $((total_5xx - injected_5xx)) -gt 0 ]]; then
      echo "INFRA_FLAKE:server_5xx"
      return 1
    fi

    # Network errors
    if grep -qi "connection refused\|connection reset\|timeout" "$http" 2>/dev/null; then
      echo "INFRA_FLAKE:network_error"
      return 1
    fi
  fi

  # Check DATA_ISSUE patterns
  if [[ -f "$validation" ]]; then
    if grep -qi "Could not fetch\|404\|not found\|fixture.*missing" "$validation" 2>/dev/null; then
      echo "DATA_ISSUE:fixture_not_found"
      return 1
    fi
  fi

  # If we get here, it's a MODEL_FAILURE (valid failure - counts against model)
  local failure_mode="unknown"

  if [[ -f "$validation" ]]; then
    if grep -qi "Completed: 0/\|completed 0 of" "$validation" 2>/dev/null; then
      failure_mode="no_completions"
    elif grep -qi "Commented: 0/\|commented 0 of" "$validation" 2>/dev/null; then
      failure_mode="no_comments"
    elif grep -qi "missing marker\|wrong marker\|marker not found" "$validation" 2>/dev/null; then
      failure_mode="wrong_marker_content"
    elif grep -qi "not after run_start\|stale\|outdated" "$validation" 2>/dev/null; then
      failure_mode="stale_data"
    elif grep -qi "only found\|expected.*found\|count mismatch" "$validation" 2>/dev/null; then
      failure_mode="incomplete_work"
    fi
  fi

  # Check for model hitting turn limit
  local max_turns
  max_turns=$(jq -r '.metrics.turns // 0' "$metrics")
  local configured_max="${BCQ_BENCH_MAX_TURNS:-50}"
  if [[ "$max_turns" -ge "$configured_max" ]]; then
    failure_mode="hit_turn_limit"
  fi

  echo "MODEL_FAILURE:$failure_mode"
  return 0  # Valid failure - counts against model
}

# === Helper: Should this class be rerun? ===
should_rerun() {
  local class="$1"
  case "$class" in
    HARNESS_BUG|INFRA_FLAKE|DATA_ISSUE)
      return 0  # Yes, rerun
      ;;
    *)
      return 1  # No, don't rerun
      ;;
  esac
}

# === Helper: Is this a valid result? ===
is_valid_class() {
  local class="$1"
  case "$class" in
    VALID|MODEL_FAILURE)
      return 0  # Valid result
      ;;
    *)
      return 1  # Invalid, don't count in metrics
      ;;
  esac
}

# === Main ===
result=$(triage_run)
failure_class="${result%%:*}"
failure_detail="${result#*:}"

# Determine run validity
if is_valid_class "$failure_class"; then
  run_status="valid"
else
  run_status="invalid"
fi

# Determine rerun recommendation
if should_rerun "$failure_class"; then
  rerun_recommended="true"
else
  rerun_recommended="false"
fi

# Update metrics.json if requested
if [[ "$UPDATE_MODE" == "true" ]]; then
  jq --arg status "$run_status" \
     --arg class "$failure_class" \
     --arg detail "$failure_detail" \
     --argjson rerun "$rerun_recommended" \
     '. + {run_status: $status, failure_class: $class, failure_detail: $detail, rerun_recommended: $rerun}' \
     "$metrics" > "${metrics}.tmp" && mv "${metrics}.tmp" "$metrics"
fi

# Output result
echo "$result"
