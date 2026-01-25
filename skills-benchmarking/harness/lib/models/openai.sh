#!/usr/bin/env bash
# OpenAI GPT API adapter
# Implements model_* interface for the benchmark harness
#
# NOTE: This adapter uses Chat Completions API. GPT-5.* models also work with
# the newer Responses API which provides better caching and reasoning support.
# For production benchmarks, consider migrating to Responses API for GPT-5.2+.
# See: https://platform.openai.com/docs/guides/migrate-to-responses

OPENAI_API_KEY="${OPENAI_API_KEY:-}"
OPENAI_MODEL=""

model_init() {
  local model="$1"

  [[ -z "$OPENAI_API_KEY" ]] && { log_error "OPENAI_API_KEY not set"; exit 1; }

  # Map friendly names to API model IDs
  case "$model" in
    gpt*|o1*|o3*|o4*)
      OPENAI_MODEL="$model"
      ;;
    openai/*)
      OPENAI_MODEL="${model#openai/}"
      ;;
    *)
      OPENAI_MODEL="$model"
      ;;
  esac

  log_info "OpenAI adapter: $OPENAI_MODEL (Chat Completions)"
}

model_call() {
  local system_prompt="$1"
  local messages_json="$2"

  # Build messages with system prompt prepended
  local full_messages
  full_messages=$(jq -n \
    --arg system "$system_prompt" \
    --argjson msgs "$messages_json" \
    '[{role: "system", content: $system}] + $msgs')

  # Build tools array (OpenAI format)
  local tools_json
  tools_json=$(cat <<'EOF'
[{
  "type": "function",
  "function": {
    "name": "bash",
    "description": "Execute a bash command. Use this to run bcq commands, curl requests, or other shell operations.",
    "parameters": {
      "type": "object",
      "properties": {
        "command": {
          "type": "string",
          "description": "The bash command to execute"
        }
      },
      "required": ["command"]
    }
  }
}]
EOF
)

  # Build request
  local request
  request=$(jq -n \
    --arg model "$OPENAI_MODEL" \
    --argjson messages "$full_messages" \
    --argjson tools "$tools_json" \
    '{
      model: $model,
      messages: $messages,
      tools: $tools,
      tool_choice: "auto"
    }')

  # Make API call
  local response
  response=$(curl -sS "https://api.openai.com/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -d "$request")

  # Log raw response (redact any secrets that might appear in content)
  if [[ -n "${LOG_DIR:-}" ]]; then
    echo "$response" | sed -E 's/(Bearer\s+)[A-Za-z0-9_\-\.]+/\1[REDACTED]/gi' >> "$LOG_DIR/api_responses.jsonl"
  fi

  echo "$response"
}

model_parse_tools() {
  local response="$1"

  # Extract tool_calls from first choice
  echo "$response" | jq -c '[.choices[0].message.tool_calls // [] | .[] | {id: .id, name: .function.name, input: (.function.arguments | fromjson)}]'
}

model_stop_reason() {
  local response="$1"
  echo "$response" | jq -r '.choices[0].finish_reason // "unknown"'
}

model_metrics() {
  local response="$1"

  local prompt_tokens completion_tokens cached_tokens uncached_tokens
  prompt_tokens=$(echo "$response" | jq -r '.usage.prompt_tokens // 0')
  completion_tokens=$(echo "$response" | jq -r '.usage.completion_tokens // 0')
  # OpenAI returns cached tokens in prompt_tokens_details (available with GPT-4o and later)
  # NOTE: prompt_tokens includes cached tokens, so subtract to avoid double-counting
  cached_tokens=$(echo "$response" | jq -r '.usage.prompt_tokens_details.cached_tokens // 0')
  uncached_tokens=$((prompt_tokens - cached_tokens))
  (( uncached_tokens < 0 )) && uncached_tokens=0

  METRICS[input_tokens]=$((METRICS[input_tokens] + uncached_tokens))
  METRICS[output_tokens]=$((METRICS[output_tokens] + completion_tokens))
  METRICS[cache_read_tokens]=$((METRICS[cache_read_tokens] + cached_tokens))
}

model_format_assistant() {
  local response="$1"

  # Extract assistant message from response
  echo "$response" | jq -c '.choices[0].message | {role: "assistant", content: .content, tool_calls: .tool_calls}'
}

model_format_tool_result() {
  local tool_id="$1"
  local tool_name="$2"
  local result="$3"
  local exit_code="$4"

  # Format tool result for OpenAI API
  local content
  if (( exit_code == 0 )); then
    content="$result"
  else
    content="Error (exit code $exit_code): $result"
  fi

  jq -n -c \
    --arg tool_call_id "$tool_id" \
    --arg content "$content" \
    '{role: "tool", tool_call_id: $tool_call_id, content: $content}'
}
