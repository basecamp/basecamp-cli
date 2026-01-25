#!/usr/bin/env bash
# Anthropic Claude API adapter
# Implements model_* interface for the benchmark harness

ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}"
ANTHROPIC_MODEL=""
ANTHROPIC_THINKING="${ANTHROPIC_THINKING:-false}"

model_init() {
  local model="$1"

  [[ -z "$ANTHROPIC_API_KEY" ]] && { log_error "ANTHROPIC_API_KEY not set"; exit 1; }

  # Map friendly names to API model IDs
  # Default aliases point to Claude 4.5 (current generation)
  case "$model" in
    # Claude 4.5 (current) - default for aliases
    claude-sonnet|claude-sonnet-4.5)
      ANTHROPIC_MODEL="claude-sonnet-4-5-20250929"
      ;;
    claude-haiku|claude-haiku-4.5)
      ANTHROPIC_MODEL="claude-haiku-4-5-20251001"
      ;;
    claude-opus|claude-opus-4.5)
      ANTHROPIC_MODEL="claude-opus-4-5-20251101"
      ANTHROPIC_THINKING=true
      ;;
    # Claude 4 (legacy) - explicit version required
    claude-sonnet-4|claude-sonnet-4.0)
      ANTHROPIC_MODEL="claude-sonnet-4-20250514"
      ;;
    claude-opus-4|claude-opus-4.0)
      ANTHROPIC_MODEL="claude-opus-4-20250514"
      ANTHROPIC_THINKING=true
      ;;
    claude-opus-4.1)
      ANTHROPIC_MODEL="claude-opus-4-1-20250805"
      ANTHROPIC_THINKING=true
      ;;
    # Claude 3.x (legacy)
    claude-3-haiku|claude-3.5-haiku)
      ANTHROPIC_MODEL="claude-3-5-haiku-20241022"
      ;;
    claude-3-haiku-legacy)
      ANTHROPIC_MODEL="claude-3-haiku-20240307"
      ;;
    # Pass-through for explicit model IDs
    anthropic/*)
      ANTHROPIC_MODEL="${model#anthropic/}"
      ;;
    *)
      ANTHROPIC_MODEL="$model"
      ;;
  esac

  log_info "Anthropic adapter: $ANTHROPIC_MODEL (thinking=$ANTHROPIC_THINKING)"
}

model_call() {
  local system_prompt="$1"
  local messages_json="$2"

  # Build tools array
  local tools_json
  tools_json=$(cat <<'EOF'
[{
  "name": "bash",
  "description": "Execute a bash command. Use this to run bcq commands, curl requests, or other shell operations.",
  "input_schema": {
    "type": "object",
    "properties": {
      "command": {
        "type": "string",
        "description": "The bash command to execute"
      }
    },
    "required": ["command"]
  }
}]
EOF
)

  # Build request
  local request
  local max_tokens=8192
  # Claude 3 Haiku has 4096 output limit
  [[ "$ANTHROPIC_MODEL" == *"haiku"* ]] && max_tokens=4096

  if [[ "$ANTHROPIC_THINKING" == "true" ]]; then
    # Extended thinking request
    request=$(jq -n \
      --arg model "$ANTHROPIC_MODEL" \
      --arg system "$system_prompt" \
      --argjson messages "$messages_json" \
      --argjson tools "$tools_json" \
      --argjson budget_tokens 10000 \
      '{
        model: $model,
        max_tokens: 16000,
        thinking: {
          type: "enabled",
          budget_tokens: $budget_tokens
        },
        system: $system,
        messages: $messages,
        tools: $tools
      }')
  else
    request=$(jq -n \
      --arg model "$ANTHROPIC_MODEL" \
      --arg system "$system_prompt" \
      --argjson messages "$messages_json" \
      --argjson tools "$tools_json" \
      --argjson max_tokens "$max_tokens" \
      '{
        model: $model,
        max_tokens: $max_tokens,
        system: $system,
        messages: $messages,
        tools: $tools
      }')
  fi

  # Make API call
  local headers=(-H "Content-Type: application/json" \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01")

  if [[ "$ANTHROPIC_THINKING" == "true" ]]; then
    headers+=(-H "anthropic-beta: interleaved-thinking-2025-05-14")
  fi

  local response
  response=$(curl -sS "https://api.anthropic.com/v1/messages" \
    "${headers[@]}" \
    -d "$request")

  # Log raw response for debugging (redact any secrets that might appear in content)
  if [[ -n "${LOG_DIR:-}" ]]; then
    echo "$response" | sed -E 's/(Bearer\s+)[A-Za-z0-9_\-\.]+/\1[REDACTED]/gi' >> "$LOG_DIR/api_responses.jsonl"
  fi

  echo "$response"
}

model_parse_tools() {
  local response="$1"

  # Extract tool_use blocks from content array
  echo "$response" | jq -c '[.content[]? | select(.type == "tool_use") | {id: .id, name: .name, input: .input}]'
}

model_stop_reason() {
  local response="$1"
  echo "$response" | jq -r '.stop_reason // "unknown"'
}

model_metrics() {
  local response="$1"

  # Extract usage from response
  local input_tokens output_tokens cache_read cache_write
  input_tokens=$(echo "$response" | jq -r '.usage.input_tokens // 0')
  output_tokens=$(echo "$response" | jq -r '.usage.output_tokens // 0')
  cache_read=$(echo "$response" | jq -r '.usage.cache_read_input_tokens // 0')
  cache_write=$(echo "$response" | jq -r '.usage.cache_creation_input_tokens // 0')

  METRICS[input_tokens]=$((METRICS[input_tokens] + input_tokens))
  METRICS[output_tokens]=$((METRICS[output_tokens] + output_tokens))
  METRICS[cache_read_tokens]=$((METRICS[cache_read_tokens] + cache_read))
  METRICS[cache_write_tokens]=$((METRICS[cache_write_tokens] + cache_write))
}

model_format_assistant() {
  local response="$1"

  # Format assistant response for message history
  echo "$response" | jq -c '{role: "assistant", content: .content}'
}

model_format_tool_result() {
  local tool_id="$1"
  local tool_name="$2"
  local result="$3"
  local exit_code="$4"

  # Format tool result for Anthropic API
  local content
  if (( exit_code == 0 )); then
    content="$result"
  else
    content="Error (exit code $exit_code): $result"
  fi

  jq -n -c \
    --arg tool_use_id "$tool_id" \
    --arg content "$content" \
    '{role: "user", content: [{type: "tool_result", tool_use_id: $tool_use_id, content: $content}]}'
}
