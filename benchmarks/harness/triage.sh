#!/usr/bin/env bash
# benchmarks/harness/triage.sh
# Classifies run failures and determines rerun eligibility
#
# Failure Taxonomy:
#   VALID           - Run completed correctly (success or valid model failure)
#   HARNESS_BUG     - Our fault: wrong prompt, missing env, injection failure
#   INFRA_FLAKE     - Transient: 5xx storms, auth expired, network issues
#   MODEL_FAILURE   - Model's fault: counts as valid failure against model
#   DATA_ISSUE      - Fixture problem: missing data, wrong IDs
#   API_UNAVAILABLE - Model API error, 0 tokens, immediate failure
#
# Usage:
#   ./triage.sh <run_dir>
#   ./triage.sh --batch <results_dir>  # Triage all runs
#
# Output format:
#   <CLASS>:<detail>:<rerun_recommended>
#   e.g., "MODEL_FAILURE:no_comments:false"
#        "HARNESS_BUG:single_quote_no_expansion:true"

set -euo pipefail

VERSION="1"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(dirname "$SCRIPT_DIR")"

# Classes that count as valid (results are meaningful)
VALID_CLASSES="VALID MODEL_FAILURE"

# Classes that should trigger auto-rerun
RERUN_CLASSES="HARNESS_BUG INFRA_FLAKE DATA_ISSUE"

# Sanity caps - align with run.sh defaults
# If BCQ_BENCH_MAX_TURNS is set, use it; otherwise default to 50 (same as run.sh)
SANITY_MAX_TURNS="${BCQ_BENCH_MAX_TURNS:-50}"
SANITY_MAX_OUTPUT_KB=500

is_valid_class() {
  local class="$1"
  [[ " $VALID_CLASSES " == *" $class "* ]]
}

should_rerun() {
  local class="$1"
  [[ " $RERUN_CLASSES " == *" $class "* ]]
}

# Main triage function
triage_run() {
  local run_dir="$1"
  local metrics="$run_dir/metrics.json"
  local tools="$run_dir/tools.jsonl"
  local http="$run_dir/http.jsonl"
  local validation="$run_dir/validation.txt"
  local api="$run_dir/api_responses.jsonl"

  # Verify required files exist
  if [[ ! -f "$metrics" ]]; then
    echo "HARNESS_BUG:missing_metrics:true"
    return 1
  fi

  # Already passed? Valid success.
  if jq -e '.success == true' "$metrics" >/dev/null 2>&1; then
    echo "VALID:success:false"
    return 0
  fi

  # === Check API_UNAVAILABLE first (most obvious) ===
  local tokens turns tool_calls
  tokens=$(jq -r '.metrics.tokens.total // 0' "$metrics")
  turns=$(jq -r '.metrics.turns // 0' "$metrics")
  tool_calls=$(jq -r '.metrics.tools.calls // 0' "$metrics")

  if [[ "$tokens" -eq 0 ]]; then
    echo "API_UNAVAILABLE:zero_tokens:false"
    return 1
  fi

  # turns==1 && no tool calls = API didn't even start properly
  if [[ "$turns" -eq 1 ]] && [[ "$tool_calls" -eq 0 ]]; then
    echo "API_UNAVAILABLE:single_turn_no_tools:false"
    return 1
  fi

  # Multiple turns but no tool calls = model talked but never executed
  # This is a MODEL_FAILURE, not API_UNAVAILABLE - it's valid but failed
  # (We'll classify this more specifically later in the MODEL_FAILURE section)

  # === Sanity cap: detect looping/runaway before other checks ===
  # Only flag as looping if turns EXCEED the configured max (not just reach it)
  if [[ "$turns" -gt "$SANITY_MAX_TURNS" ]]; then
    echo "MODEL_FAILURE:looping_turns_${turns}:false"
    return 0  # Valid failure - counts against model
  fi

  if [[ -f "$tools" ]]; then
    local tools_kb
    tools_kb=$(( $(wc -c < "$tools" 2>/dev/null || echo 0) / 1024 ))
    if [[ "$tools_kb" -gt "$SANITY_MAX_OUTPUT_KB" ]]; then
      echo "MODEL_FAILURE:runaway_output_${tools_kb}kb:false"
      return 0  # Valid failure - counts against model
    fi
  fi

  # === Check HARNESS_BUG patterns ===

  # Missing BCQ_ environment variables in tool output (harness setup issue)
  # Only flag as HARNESS_BUG if the missing variable is a BCQ_ variable
  # Non-BCQ variables (like TODAY) that the model invents are MODEL_FAILURE, not HARNESS_BUG
  if [[ -f "$tools" ]]; then
    local missing_var
    missing_var=$(grep -oE 'BCQ_[A-Z_]+: (not set|unbound)|BCQ_[A-Z_]+.*not set' "$tools" 2>/dev/null | head -1 || echo "")
    if [[ -n "$missing_var" ]]; then
      echo "HARNESS_BUG:env_missing:${missing_var}:true"
      return 1
    fi
  fi

  # Single quotes DIRECTLY around shell variables (prevents expansion)
  # Catches: --comment 'Processed BenchChain $BCQ_BENCH_RUN_ID'
  # Does NOT catch: bash -lc '... --comment "Processed BenchChain $BCQ_BENCH_RUN_ID" ...'
  # (In the latter, double quotes inside single-quoted bash -c still expand)
  # Look for patterns where single quote is followed by text then $BCQ with no intervening double quote
  if [[ -f "$tools" ]] && grep -qE -- "--comment[[:space:]]+'[^\"]*\\\$BCQ_BENCH" "$tools" 2>/dev/null; then
    echo "HARNESS_BUG:single_quote_no_expansion:true"
    return 1
  fi

  # Injection configured but didn't fire AND triggering request didn't happen
  # Read from metrics.json (persisted) or fall back to environment
  local injection_expected inject_match
  injection_expected=$(jq -r '.injection.expected // ""' "$metrics" 2>/dev/null)
  [[ "$injection_expected" == "null" ]] && injection_expected=""
  [[ -z "$injection_expected" ]] && injection_expected="${BCQ_BENCH_INJECTION_EXPECTED:-}"

  inject_match=$(jq -r '.injection.match // ""' "$metrics" 2>/dev/null)
  [[ "$inject_match" == "null" ]] && inject_match=""
  [[ -z "$inject_match" ]] && inject_match="${BCQ_INJECT_MATCH:-page=2}"

  if [[ -n "$injection_expected" ]] && [[ -f "$http" ]]; then
    local has_429
    has_429=$(grep -c '"http_code":"429"' "$http" 2>/dev/null | head -1 || echo 0)
    has_429="${has_429//[^0-9]/}"  # Strip non-digits
    has_429="${has_429:-0}"
    if [[ "$has_429" -eq 0 ]]; then
      # Check if the triggering request pattern even appeared
      if ! grep -q "$inject_match" "$http" 2>/dev/null; then
        echo "HARNESS_BUG:injection_not_triggered:true"
        return 1
      fi
      # Request happened but 429 not injected - injection bug
      echo "HARNESS_BUG:injection_configured_but_missing:true"
      return 1
    fi
  fi

  # === Check INFRA_FLAKE patterns ===
  if [[ -f "$http" ]]; then
    # Non-injected 5xx errors
    local total_5xx injected_5xx real_5xx
    total_5xx=$(grep -cE '"http_code":"5[0-9]{2}"' "$http" 2>/dev/null || true)
    injected_5xx=$(grep -c '"injected":true.*"http_code":"5' "$http" 2>/dev/null || true)
    total_5xx=$(echo "$total_5xx" | tr -d '[:space:]')
    injected_5xx=$(echo "$injected_5xx" | tr -d '[:space:]')
    total_5xx="${total_5xx:-0}"
    injected_5xx="${injected_5xx:-0}"
    real_5xx=$((total_5xx - injected_5xx))

    if [[ "$real_5xx" -gt 0 ]]; then
      echo "INFRA_FLAKE:server_5xx:true"
      return 1
    fi
  fi

  # API provider issues - but first check if it's actually a HARNESS_BUG
  if [[ -f "$api" ]]; then
    # Check for config errors first (HARNESS_BUG, not API issue)
    if grep -qiE "model_not_found|invalid_api_key|authentication|unauthorized" "$api" 2>/dev/null; then
      echo "HARNESS_BUG:api_config_error:true"
      return 1
    fi
    # Actual API provider issues
    if grep -qiE "overloaded|rate.limit|capacity|timeout" "$api" 2>/dev/null; then
      echo "INFRA_FLAKE:api_provider_issue:true"
      return 1
    fi
  fi

  # === Check DATA_ISSUE patterns ===
  # Check validation.txt
  if [[ -f "$validation" ]]; then
    if grep -qiE "Could not fetch|404|fixture.*not found|todoset.*missing" "$validation" 2>/dev/null; then
      echo "DATA_ISSUE:fixture_not_found:true"
      return 1
    fi
  fi
  # Also check tools.jsonl for "No todoset found" errors
  if [[ -f "$tools" ]]; then
    if grep -qiE "No todoset found|todoset.*missing|todoset.*not found" "$tools" 2>/dev/null; then
      echo "DATA_ISSUE:todoset_not_found:true"
      return 1
    fi
  fi

  # === MODEL_FAILURE - Valid failure, determine specific mode ===
  local failure_mode="unknown"

  # Check for no tool calls first (multi-turn but never executed tools)
  if [[ "$tool_calls" -eq 0 ]]; then
    failure_mode="no_tool_calls"
  fi

  if [[ -f "$validation" ]] && [[ "$failure_mode" == "unknown" ]]; then
    # Check specific failure patterns in order of specificity
    if grep -q "Completed: 0/" "$validation" 2>/dev/null; then
      # Check if HTTP shows completion attempts (any 2xx is success)
      local completion_posts
      completion_posts=$(grep -cE 'POST.*completion\.json.*"http_code":"2[0-9]{2}"' "$http" 2>/dev/null | head -1 || echo 0)
      completion_posts="${completion_posts//[^0-9]/}"
      completion_posts="${completion_posts:-0}"
      if [[ "$completion_posts" -gt 0 ]]; then
        # Server accepted write but validation found nothing - infrastructure issue
        echo "INFRA_FLAKE:write_ack_no_effect:true"
        return 1
      else
        failure_mode="no_completions"
      fi
    elif grep -q "Commented: 0/" "$validation" 2>/dev/null; then
      # Check if HTTP shows comment attempts (any 2xx is success)
      local comment_posts
      comment_posts=$(grep -cE 'POST.*comments\.json.*"http_code":"2[0-9]{2}"' "$http" 2>/dev/null | head -1 || echo 0)
      comment_posts="${comment_posts//[^0-9]/}"
      comment_posts="${comment_posts:-0}"
      if [[ "$comment_posts" -gt 0 ]]; then
        # Server accepted write but validation found nothing - infrastructure issue
        echo "INFRA_FLAKE:write_ack_no_effect:true"
        return 1
      else
        failure_mode="no_comments"
      fi
    elif grep -q "missing marker comment" "$validation" 2>/dev/null; then
      failure_mode="wrong_marker_content"
    elif grep -q "not after run_start" "$validation" 2>/dev/null; then
      failure_mode="stale_timestamps"
    elif grep -q "FAIL:" "$validation" 2>/dev/null; then
      # Generic failure - extract first failure reason
      failure_mode=$(grep "FAIL:" "$validation" | head -1 | sed 's/FAIL: //' | tr ' ' '_' | cut -c1-40)
    fi
  fi

  # Check for non-BCQ unbound variables (model invented non-existent variables)
  if [[ -f "$tools" ]] && grep -qiE "unbound variable" "$tools" 2>/dev/null; then
    # Already checked BCQ_ vars above (HARNESS_BUG), so any remaining unbound var is model error
    failure_mode="invented_nonexistent_var"
  fi

  # Check for "printed code instead of executing" pattern
  if [[ -f "$tools" ]]; then
    local tool_output_size http_requests
    tool_output_size=$(wc -c < "$tools" 2>/dev/null || echo 0)
    http_requests=$(wc -l < "$http" 2>/dev/null || echo 0)

    # Large tool output but few HTTP requests suggests code was printed not executed
    if [[ "$tool_output_size" -gt 5000 ]] && [[ "$http_requests" -lt 5 ]]; then
      failure_mode="printed_code_not_executed"
    fi
  fi

  # Check for PAGINATION_MISS - no todolists.json request (didn't enumerate lists)
  if [[ -f "$http" ]] && [[ "$failure_mode" == "unknown" ]]; then
    if ! grep -q 'todolists\.json' "$http" 2>/dev/null; then
      failure_mode="pagination_miss"
    fi
  fi

  echo "MODEL_FAILURE:$failure_mode:false"
  return 0  # Valid failure - counts against model
}

# Batch triage all runs in a directory
batch_triage() {
  local results_dir="$1"
  local summary_valid=0
  local summary_invalid=0
  local -A class_counts

  echo "=== Triaging runs in $results_dir ==="
  echo ""

  for run_dir in "$results_dir"/*/; do
    [[ -d "$run_dir" ]] || continue
    [[ -f "$run_dir/metrics.json" ]] || continue

    local run_id
    run_id=$(basename "$run_dir")
    local result
    result=$(triage_run "$run_dir" 2>/dev/null || echo "ERROR:triage_failed:true")

    local class="${result%%:*}"
    local rest="${result#*:}"
    local detail="${rest%%:*}"
    local rerun="${rest##*:}"

    # Update counts
    class_counts["$class"]=$((${class_counts["$class"]:-0} + 1))

    if is_valid_class "$class"; then
      summary_valid=$((summary_valid + 1))
    else
      summary_invalid=$((summary_invalid + 1))
    fi

    # Output per-run result
    local status
    is_valid_class "$class" && status="valid" || status="INVALID"
    printf "%-50s %s %-20s %s\n" "$run_id" "$status" "$class" "$detail"
  done

  echo ""
  echo "=== Summary ==="
  echo "Valid runs: $summary_valid"
  echo "Invalid runs: $summary_invalid"
  echo ""
  echo "By class:"
  for class in "${!class_counts[@]}"; do
    printf "  %-20s %d\n" "$class:" "${class_counts[$class]}"
  done
}

# Update metrics.json with triage results
update_metrics() {
  local run_dir="$1"
  local result
  result=$(triage_run "$run_dir") || true  # Continue even if triage_run returns non-zero

  local class="${result%%:*}"
  local rest="${result#*:}"
  local detail="${rest%%:*}"
  local rerun="${rest##*:}"

  local status
  is_valid_class "$class" && status="valid" || status="invalid"

  local metrics="$run_dir/metrics.json"
  if [[ -f "$metrics" ]]; then
    jq --arg class "$class" \
       --arg detail "$detail" \
       --arg status "$status" \
       --argjson rerun "$rerun" \
       '. + {run_status: $status, failure_class: $class, failure_detail: $detail, rerun_recommended: $rerun}' \
       "$metrics" > "$metrics.tmp" && mv "$metrics.tmp" "$metrics"
  fi

  echo "$result"
}

# Main
main() {
  case "${1:-}" in
    --batch)
      batch_triage "${2:-.}"
      ;;
    --update)
      update_metrics "${2:-.}"
      ;;
    --help|-h)
      cat << 'EOF'
Usage: triage.sh <run_dir>           Triage single run
       triage.sh --batch <dir>       Triage all runs in directory
       triage.sh --update <run_dir>  Triage and update metrics.json

Output format: <CLASS>:<detail>:<rerun_recommended>

Classes:
  VALID           - Successful run
  MODEL_FAILURE   - Valid failure (counts against model)
  HARNESS_BUG     - Our bug (rerun after fix)
  INFRA_FLAKE     - Transient issue (auto-rerun)
  DATA_ISSUE      - Fixture problem (rerun after fix)
  API_UNAVAILABLE - Model API issue (exclude)
EOF
      ;;
    *)
      if [[ -d "$1" ]]; then
        triage_run "$1"
      else
        echo "Error: $1 is not a directory" >&2
        exit 1
      fi
      ;;
  esac
}

main "$@"
