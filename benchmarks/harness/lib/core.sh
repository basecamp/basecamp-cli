#!/usr/bin/env bash
# Benchmark harness core - agentic loop and tool dispatch
# Provides model-agnostic agent execution with full instrumentation

set -euo pipefail

# === Portability ===
# Use gtimeout on macOS, timeout on Linux
if command -v gtimeout &>/dev/null; then
  TIMEOUT_CMD="gtimeout"
elif command -v timeout &>/dev/null; then
  TIMEOUT_CMD="timeout"
else
  # Fallback: no timeout (warn user)
  TIMEOUT_CMD=""
fi

# === Metrics Collection ===
declare -g -A METRICS=(
  [input_tokens]=0
  [output_tokens]=0
  [cache_read_tokens]=0
  [cache_write_tokens]=0
  [api_calls]=0
  [api_latency_ms]=0
  [tool_calls]=0
  [tool_errors]=0
  [http_requests]=0
  [http_429s]=0
  [turns]=0
  [start_time]=0
  [end_time]=0
  [prompt_system_bytes]=0
  [prompt_task_bytes]=0
)

# === Agentic Loop ===

run_agent() {
  local system_prompt="$1"
  local user_prompt="$2"
  local max_turns="${3:-50}"

  # Track prompt sizes
  METRICS[prompt_system_bytes]=${#system_prompt}
  METRICS[prompt_task_bytes]=${#user_prompt}

  # Initialize conversation
  local -a messages=()
  messages+=("$(jq -n --arg content "$user_prompt" '{role: "user", content: $content}')")

  local turn=0
  local stop_reason=""

  while (( turn < max_turns )); do
    ((turn++))
    METRICS[turns]=$turn

    log_debug "Turn $turn/$max_turns"

    # Call model API
    local response
    local call_start=$(_now_ms)
    response=$(model_call "$system_prompt" "$(printf '%s\n' "${messages[@]}" | jq -s '.')")
    local call_end=$(_now_ms)

    ((METRICS[api_calls]++))
    METRICS[api_latency_ms]=$((METRICS[api_latency_ms] + call_end - call_start))

    # Extract and accumulate token metrics
    model_metrics "$response"

    # Log conversation turn
    log_conversation "$turn" "assistant" "$response"

    # Check stop reason
    stop_reason=$(model_stop_reason "$response")

    # Parse tool calls
    local tool_calls
    tool_calls=$(model_parse_tools "$response")

    if [[ -z "$tool_calls" ]] || [[ "$tool_calls" == "[]" ]] || [[ "$tool_calls" == "null" ]]; then
      log_info "Agent completed: $stop_reason"
      break
    fi

    # Add assistant message to history
    messages+=("$(model_format_assistant "$response")")

    # Execute each tool call
    local -a tool_results=()
    while IFS= read -r tool_call; do
      [[ -z "$tool_call" ]] && continue

      local tool_id tool_name tool_input
      tool_id=$(echo "$tool_call" | jq -r '.id // ""')
      tool_name=$(echo "$tool_call" | jq -r '.name')
      tool_input=$(echo "$tool_call" | jq -c '.input')

      log_debug "Tool: $tool_name"

      # Execute tool
      local result exit_code
      result=$(execute_tool "$tool_name" "$tool_input" 2>&1) || exit_code=$?
      exit_code=${exit_code:-0}

      ((METRICS[tool_calls]++))
      if (( exit_code != 0 )); then
        ((METRICS[tool_errors]++))
      fi

      # Format result for model
      tool_results+=("$(model_format_tool_result "$tool_id" "$tool_name" "$result" "$exit_code")")

      # Log tool execution
      log_tool "$turn" "$tool_name" "$tool_input" "$result" "$exit_code"

    done < <(echo "$tool_calls" | jq -c '.[]')

    # Add tool results to messages
    for result in "${tool_results[@]}"; do
      messages+=("$result")
    done

  done

  if (( turn >= max_turns )); then
    log_warn "Max turns reached: $max_turns"
  fi

  # Save metrics to file (since run_agent may run in a subshell)
  save_metrics

  # Return final assistant response
  echo "$response" | jq -r '.content // .choices[0].message.content // ""' | head -c 2000
}

# === Metrics Persistence ===
# Since run_agent often runs in a subshell ($(...)), we save metrics to a file
# and load them back in the parent shell

save_metrics() {
  local metrics_file="${LOG_DIR:-.}/.metrics_tmp"
  cat > "$metrics_file" <<EOF
METRICS[input_tokens]=${METRICS[input_tokens]}
METRICS[output_tokens]=${METRICS[output_tokens]}
METRICS[cache_read_tokens]=${METRICS[cache_read_tokens]}
METRICS[cache_write_tokens]=${METRICS[cache_write_tokens]}
METRICS[api_calls]=${METRICS[api_calls]}
METRICS[api_latency_ms]=${METRICS[api_latency_ms]}
METRICS[tool_calls]=${METRICS[tool_calls]}
METRICS[tool_errors]=${METRICS[tool_errors]}
METRICS[turns]=${METRICS[turns]}
EOF
}

load_metrics() {
  local metrics_file="${LOG_DIR:-.}/.metrics_tmp"
  if [[ -f "$metrics_file" ]]; then
    source "$metrics_file"
    rm -f "$metrics_file"
  fi
}

# === Tool Execution ===

execute_tool() {
  local name="$1"
  local input="$2"

  case "$name" in
    bash|shell|computer|execute)
      execute_bash_tool "$input"
      ;;
    *)
      echo "Unknown tool: $name"
      return 1
      ;;
  esac
}

execute_bash_tool() {
  local input="$1"

  # Extract command from various input formats
  local command
  command=$(echo "$input" | jq -r '.command // .cmd // .' 2>/dev/null || echo "$input")

  # Security: apply tool filter if set
  if [[ -n "${TOOL_FILTER:-}" ]]; then
    if ! [[ "$command" =~ $TOOL_FILTER ]]; then
      echo "Command blocked by filter: ${command:0:100}"
      return 1
    fi
  fi

  # Use homebrew bash for bash 4+ compatibility (required by bcq)
  local bash_cmd="/opt/homebrew/bin/bash"
  [[ ! -x "$bash_cmd" ]] && bash_cmd="bash"

  # Execute with timeout (if available)
  if [[ -n "$TIMEOUT_CMD" ]]; then
    "$TIMEOUT_CMD" 60 "$bash_cmd" -c "$command" 2>&1 || return $?
  else
    "$bash_cmd" -c "$command" 2>&1 || return $?
  fi
}

# === HTTP Request Counting ===
# Called after agent completes to count HTTP requests from log

count_http_requests() {
  local log_file="${BCQ_BENCH_LOGFILE:-}"

  if [[ -f "$log_file" ]]; then
    METRICS[http_requests]=$(wc -l < "$log_file" | tr -d ' ')
    METRICS[http_429s]=$(grep -c '"http_code":"429"\|"http_code":429' "$log_file" 2>/dev/null) || METRICS[http_429s]=0
  fi
}
