#!/usr/bin/env bash
# Benchmark harness logging - structured output and metrics

set -euo pipefail

# === Portability ===
# macOS date doesn't support %N, use gdate if available
_now_ms() {
  if command -v gdate &>/dev/null; then
    gdate +%s%3N
  else
    # Fall back to seconds * 1000 (no sub-second precision)
    echo "$(($(date +%s) * 1000))"
  fi
}

# === Configuration ===
LOG_LEVEL="${LOG_LEVEL:-info}"  # debug, info, warn, error
LOG_DIR=""
CONVERSATION_LOG=""
TOOL_LOG=""
RUN_ID=""
RUN_START=""  # ISO 8601 timestamp for time-based validation

# === Initialization ===

init_logging() {
  local task="$1"
  local strategy="$2"
  local model="$3"
  local results_dir="$4"

  # Generate run ID
  local model_safe="${model//[:\/]/-}"
  RUN_ID="$(date +%Y%m%d-%H%M%S)-${task}-${strategy}-${model_safe}"

  # Create log directory
  LOG_DIR="$results_dir/$RUN_ID"
  mkdir -p "$LOG_DIR"

  # Set up log files
  CONVERSATION_LOG="$LOG_DIR/conversation.jsonl"
  TOOL_LOG="$LOG_DIR/tools.jsonl"

  # Export HTTP log path for curl wrapper
  export BCQ_BENCH_LOGFILE="$LOG_DIR/http.jsonl"

  # Record run start time (ISO 8601 for API queries)
  RUN_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  # Export for task prompts and validation
  export RUN_ID
  export RUN_START
  export BCQ_BENCH_RUN_ID="$RUN_ID"
  export BCQ_BENCH_RUN_START="$RUN_START"

  # Record start time (ms for duration calc)
  METRICS[start_time]=$(_now_ms)

  log_info "Run ID: $RUN_ID"
  log_info "Run Start: $RUN_START"
  log_info "Logs: $LOG_DIR"
}

# === Log Functions ===

_log() {
  local level="$1"
  local msg="$2"
  local ts=$(date +%H:%M:%S)
  echo "[$ts] [$level] $msg" >&2
}

log_debug() {
  [[ "$LOG_LEVEL" == "debug" ]] && _log "DEBUG" "$1" || true
}

log_info() {
  [[ "$LOG_LEVEL" =~ ^(debug|info)$ ]] && _log "INFO" "$1" || true
}

log_warn() {
  [[ "$LOG_LEVEL" =~ ^(debug|info|warn)$ ]] && _log "WARN" "$1" || true
}

log_error() {
  _log "ERROR" "$1"
}

# === Secret Redaction ===

redact_secrets() {
  local text="$1"
  # Redact Bearer tokens
  text=$(echo "$text" | sed -E 's/(Bearer\s+)[A-Za-z0-9_\-\.]+/\1[REDACTED]/gi')
  # Redact Authorization headers
  text=$(echo "$text" | sed -E 's/(Authorization:\s*)[^\s"'\'']+/\1[REDACTED]/gi')
  # Redact BASECAMP_TOKEN values
  text=$(echo "$text" | sed -E 's/(BASECAMP_TOKEN=)[^\s"'\'']+/\1[REDACTED]/gi')
  # Redact common API key patterns
  text=$(echo "$text" | sed -E 's/(sk-ant-)[A-Za-z0-9_\-]+/\1[REDACTED]/gi')
  text=$(echo "$text" | sed -E 's/(sk-)[A-Za-z0-9_\-]{20,}/\1[REDACTED]/gi')
  echo "$text"
}

# === Structured Logging ===

log_conversation() {
  local turn="$1"
  local role="$2"
  local content="$3"

  # Truncate content for logging
  local content_preview
  content_preview=$(echo "$content" | jq -c '.' 2>/dev/null | head -c 500 || echo "${content:0:500}")
  # Redact secrets
  content_preview=$(redact_secrets "$content_preview")

  jq -n -c \
    --argjson turn "$turn" \
    --arg role "$role" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg content "$content_preview" \
    '{ts: $ts, turn: $turn, role: $role, content: $content}' >> "$CONVERSATION_LOG"
}

log_tool() {
  local turn="$1"
  local name="$2"
  local input="$3"
  local output="$4"
  local exit_code="$5"

  # Truncate for logging
  local input_preview="${input:0:500}"
  local output_preview="${output:0:1000}"

  # Redact secrets from both input and output
  input_preview=$(redact_secrets "$input_preview")
  output_preview=$(redact_secrets "$output_preview")

  jq -n -c \
    --argjson turn "$turn" \
    --arg name "$name" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg input "$input_preview" \
    --arg output "$output_preview" \
    --argjson exit_code "$exit_code" \
    '{ts: $ts, turn: $turn, tool: $name, input: $input, output: $output, exit_code: $exit_code}' >> "$TOOL_LOG"
}

# === Results ===

finalize_results() {
  local success="$1"

  METRICS[end_time]=$(_now_ms)
  local duration_ms=$((METRICS[end_time] - METRICS[start_time]))

  # Count HTTP requests from log
  count_http_requests

  # Build result JSON
  local result
  result=$(jq -n \
    --arg run_id "$RUN_ID" \
    --arg run_start "$RUN_START" \
    --arg task "$TASK" \
    --arg strategy "$STRATEGY" \
    --arg model "$MODEL" \
    --argjson success "$success" \
    --argjson duration_ms "$duration_ms" \
    --argjson turns "${METRICS[turns]}" \
    --argjson input_tokens "${METRICS[input_tokens]}" \
    --argjson output_tokens "${METRICS[output_tokens]}" \
    --argjson cache_read_tokens "${METRICS[cache_read_tokens]}" \
    --argjson cache_write_tokens "${METRICS[cache_write_tokens]}" \
    --argjson api_calls "${METRICS[api_calls]}" \
    --argjson api_latency_ms "${METRICS[api_latency_ms]}" \
    --argjson tool_calls "${METRICS[tool_calls]}" \
    --argjson tool_errors "${METRICS[tool_errors]}" \
    --argjson http_requests "${METRICS[http_requests]}" \
    --argjson http_429s "${METRICS[http_429s]}" \
    --argjson prompt_system_bytes "${METRICS[prompt_system_bytes]}" \
    --argjson prompt_task_bytes "${METRICS[prompt_task_bytes]}" \
    --arg prompt_hash "${BCQ_BENCH_PROMPT_HASH:-}" \
    --arg prompt_path "${BCQ_BENCH_PROMPT_PATH:-}" \
    --arg run_start_ts "$RUN_START" \
    --arg injection_expected "${BCQ_BENCH_INJECTION_EXPECTED:-}" \
    --arg inject_match "${BCQ_INJECT_MATCH:-}" \
    --arg contract_version "${BCQ_BENCH_CONTRACT_VERSION:-1}" \
    --arg prompt_regime "${BCQ_BENCH_PROMPT_REGIME:-baseline}" \
    '{
      run_id: $run_id,
      task: $task,
      strategy: $strategy,
      model: $model,
      success: $success,
      prompt_hash: $prompt_hash,
      prompt_path: $prompt_path,
      contract_version: $contract_version,
      prompt_regime: $prompt_regime,
      run_start: $run_start_ts,
      metrics: {
        duration_ms: $duration_ms,
        turns: $turns,
        tokens: {
          input: $input_tokens,
          output: $output_tokens,
          cache_read: $cache_read_tokens,
          cache_write: $cache_write_tokens,
          total: ($input_tokens + $output_tokens)
        },
        api: {
          calls: $api_calls,
          latency_ms: $api_latency_ms
        },
        tools: {
          calls: $tool_calls,
          errors: $tool_errors
        },
        http: {
          requests: $http_requests,
          rate_limited: $http_429s
        },
        prompt: {
          system_bytes: $prompt_system_bytes,
          task_bytes: $prompt_task_bytes,
          total_bytes: ($prompt_system_bytes + $prompt_task_bytes)
        }
      },
      injection: {
        expected: $injection_expected,
        match: $inject_match
      }
    }')

  # Write metrics file
  echo "$result" > "$LOG_DIR/metrics.json"

  # Print summary
  log_info "=== Results ==="
  echo "$result" | jq '.'

  echo "$result"
}
