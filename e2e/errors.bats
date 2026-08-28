#!/usr/bin/env bats
# errors.bats - Test error handling

load test_helper


# Flag parsing errors

@test "todos --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todos --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "todos --list is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --list was removed; todolist is set via config or interactive selection
  run basecamp todos --list
  assert_failure
  assert_output_contains "Unknown option"
}

@test "todos --assignee is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --assignee was removed; use basecamp reports assigned instead
  run basecamp todos --assignee
  assert_failure
  assert_output_contains "Unknown option"
}

@test "chat --room without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat messages --room
  assert_failure
  assert_output_contains "--room requires a value"
}

@test "chat --chat is rejected after rename to --room" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat messages --chat 123
  assert_failure
  assert_output_contains "Unknown option"
}

@test "comments create without recording ID shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp comments create
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "cards --column is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --column was removed; use 'basecamp cards column' subcommand instead
  run basecamp cards --column
  assert_failure
  assert_output_contains "Unknown option"
}

@test "recordings --type without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings --type
  assert_failure
  assert_output_contains "--type requires a value"
}

@test "recordings --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Global flag errors

@test "basecamp --project without value shows error" {
  create_credentials

  run basecamp --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "basecamp --account without value shows error" {
  create_credentials

  run basecamp --account
  assert_failure
  assert_output_contains "--account requires a value"
}

@test "basecamp --cache-dir without value shows error" {
  create_credentials

  run basecamp --cache-dir
  assert_failure
  assert_output_contains "--cache-dir requires a value"
}


# JQ flag errors

@test "--jq invalid expression shows error" {
  create_credentials
  run basecamp --jq '.[invalid'
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains "invalid --jq expression"
}

@test "--jq conflicts with --ids-only" {
  create_credentials
  run basecamp --jq '.data' --ids-only
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'cannot use --jq with --ids-only'
}


# Missing content errors

@test "todo create without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos create
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "comments create without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp comments create 123
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "chat post without message shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat post
  assert_failure
  assert_json_value '.error' '<message> required'
  assert_json_value '.code' 'usage'
}


# Whitespace-only content errors

@test "todos create with whitespace-only content shows help" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos create " "
  assert_success
  assert_output_contains "basecamp todos create"
}

@test "cards create with whitespace-only title shows help" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards create " "
  assert_success
  assert_output_contains "basecamp cards create"
}

@test "messages create with whitespace-only title shows help" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messages create " "
  assert_success
  assert_output_contains "basecamp messages create"
}

@test "comments create with whitespace-only content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments create 123 " "
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}


# Missing context errors

@test "todos without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command without project shows help with subcommand list
  run basecamp todos
  assert_success
  assert_output_contains "COMMANDS"
}

@test "cards without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command without project shows help with subcommand list
  run basecamp cards
  assert_success
  assert_output_contains "COMMANDS"
}

@test "recordings without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings
  assert_failure
  assert_json_value '.error' '<type> required'
  assert_json_value '.code' 'usage'
}


# Show command argument parsing

@test "show handles flags before positional" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Should not treat --project value as ID
  run basecamp show --project 123 todo 456
  # Will fail on API call, but should parse correctly (not "Invalid assignee")
  assert_output_not_contains "Unknown option"
}

@test "todolists show handles --in flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todolists show --in 123 456
  # Will fail on API call, but should parse correctly
  assert_output_not_contains "Unknown option"
}

@test "show --help lists card-table type" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp show --help
  assert_success
  assert_output_contains "card-table"
}

@test "show unknown type error mentions card-table" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp show foobar 456
  assert_failure
  assert_output_contains "Unknown type: foobar"
  assert_output_contains "card-table"
}

@test "show card-table parses type correctly" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Will fail on API call (no project), but should parse card-table type correctly
  run basecamp show card-table 456 --project 123
  assert_output_not_contains "Unknown type"
}

# Assignee validation

@test "email assignee is passed to resolver" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123, "todolist_id": 456}'

  # Email assignees are valid input and passed to ResolvePerson
  # With a fake account, this will fail on API call (not input validation)
  run basecamp todos create "test" --assignee "john@example.com"
  assert_failure
  # Should NOT fail with "Invalid assignee" - emails are valid
  assert_output_not_contains "Invalid assignee"
}

@test "search without query shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp search
  assert_failure
  assert_json_value '.error' '<query> required'
  assert_json_value '.code' 'usage'
}

@test "todos uncomplete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos uncomplete
  assert_failure
  assert_json_value '.error' '<id|url>... required'
  assert_json_value '.code' 'usage'
}

@test "todos position without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos position --to 1
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "todos position without --to shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos position 123
  assert_failure
  assert_output_contains "required"
}

@test "todolists position without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todolists position --to 1
  assert_failure
  assert_json_value '.error' '<id|url>... required'
  assert_json_value '.code' 'usage'
}

@test "todolists position without --to shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todolists position 123
  assert_failure
  assert_output_contains "required"
}

@test "todolists position multiple ids with --to 2 shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todolists position 123 456 --to 2
  assert_failure
  assert_output_contains "position 1"
}

@test "todolists position with only a comma shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todolists position ","
  assert_failure
  assert_json_value '.error' '<id|url>... required'
  assert_json_value '.code' 'usage'
}

@test "comments without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # Group command (no RunE) shows subcommand help
  run basecamp comments
  assert_success
  assert_output_contains "COMMANDS"
}

@test "comments show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "comments update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments update
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "comments update without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments update 123
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "messages without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command (no RunE) shows subcommand help
  run basecamp messages
  assert_success
  assert_output_contains "COMMANDS"
}

@test "messages --json shows structured JSON help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messages --json
  assert_success
  assert_json_value '.command' 'messages'
  assert_output_contains '"subcommands"'
}

@test "messages create without subject shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messages create
  assert_failure
  assert_json_value '.error' '<title> required'
  assert_json_value '.code' 'usage'
}

# Search JSON cleanliness

@test "search --json outputs clean JSON to stdout" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # The info messages should go to stderr, stdout should be empty or JSON only
  run bash -c "basecamp search todos --json 2>/dev/null"
  # If there's output, it should be valid JSON (starts with { or [)
  if [[ -n "$output" ]]; then
    assert_output_starts_with '{'
  fi
}


# JSON error envelope structure

@test "error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todos --project
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_json_value '.retryable' 'false'
  assert_output_contains '"error"'
}

# Every error envelope carries "retryable" so a consumer can tell a transient
# failure from a verdict without parsing the code or message. A stub API that
# answers 503 exercises the SDK classification end to end; a mutation is used
# because the SDK retries idempotent reads on its own schedule.

start_unavailable_api_stub() {
  UNAVAILABLE_STUB_PORT_FILE="$TEST_TEMP_DIR/unavailable-stub.port"
  local log_file="$TEST_TEMP_DIR/unavailable-stub.log"

  local python_bin
  if command -v python3 >/dev/null 2>&1; then
    python_bin=python3
  elif command -v python >/dev/null 2>&1; then
    python_bin=python
  else
    echo "Error: neither python3 nor python is available in PATH; cannot start unavailable API stub" >&2
    return 1
  fi

  "$python_bin" - <<'PY' "$UNAVAILABLE_STUB_PORT_FILE" >"$log_file" 2>&1 3>&- &
import http.server
import socketserver
import sys

port_file = sys.argv[1]

class Handler(http.server.BaseHTTPRequestHandler):
    def _unavailable(self):
        self.send_response(503)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(b'{"error":"service unavailable"}')

    do_GET = _unavailable
    do_POST = _unavailable

    def log_message(self, format, *args):
        pass

with socketserver.TCPServer(('127.0.0.1', 0), Handler) as server:
    with open(port_file, 'w', encoding='utf-8') as f:
        f.write(str(server.server_address[1]))
    server.serve_forever()
PY
  UNAVAILABLE_STUB_PID=$!

  for _ in $(seq 1 50); do
    [[ -s "$UNAVAILABLE_STUB_PORT_FILE" ]] && break
    sleep 0.1
  done

  if [[ ! -s "$UNAVAILABLE_STUB_PORT_FILE" ]]; then
    echo "failed to start unavailable API stub. Log:" >&2
    cat "$log_file" >&2
    stop_unavailable_api_stub
    return 1
  fi

  export BASECAMP_BASE_URL="http://127.0.0.1:$(cat "$UNAVAILABLE_STUB_PORT_FILE")"
}

stop_unavailable_api_stub() {
  if [[ -n "${UNAVAILABLE_STUB_PID:-}" ]]; then
    kill "$UNAVAILABLE_STUB_PID" 2>/dev/null || true
    wait "$UNAVAILABLE_STUB_PID" 2>/dev/null || true
    unset UNAVAILABLE_STUB_PID
  fi
}

@test "transient API failure returns retryable JSON envelope" {
  start_unavailable_api_stub
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp projects create "Launch plan" --json
  stop_unavailable_api_stub

  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'api_error'
  assert_json_value '.retryable' 'true'
}
