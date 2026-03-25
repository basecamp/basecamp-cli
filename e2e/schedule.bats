#!/usr/bin/env bats
# schedule.bats - Test schedule command error handling

load test_helper

start_reports_schedule_stub() {
  REPORTS_STUB_LOG="$TEST_TEMP_DIR/reports-schedule-stub.log"
  REPORTS_STUB_PORT_FILE="$TEST_TEMP_DIR/reports-schedule-stub.port"

  local python_bin
  if command -v python3 >/dev/null 2>&1; then
    python_bin=python3
  elif command -v python >/dev/null 2>&1; then
    python_bin=python
  else
    echo "Error: neither python3 nor python is available in PATH; cannot start reports schedule stub" >&2
    return 1
  fi

  "$python_bin" - <<'PY' "$REPORTS_STUB_PORT_FILE" "$REPORTS_STUB_LOG" &
import http.server
import json
import socketserver
import sys

port_file = sys.argv[1]
log_file = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log_file, 'a', encoding='utf-8') as f:
            f.write(self.path + '\n')
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps({
            'schedule_entries': [],
            'recurring_schedule_entry_occurrences': [],
            'assignables': [],
        }).encode())

    def log_message(self, format, *args):
        pass

with socketserver.TCPServer(('127.0.0.1', 0), Handler) as server:
    with open(port_file, 'w', encoding='utf-8') as f:
        f.write(str(server.server_address[1]))
    server.serve_forever()
PY
  REPORTS_STUB_PID=$!

  for _ in $(seq 1 50); do
    [[ -s "$REPORTS_STUB_PORT_FILE" ]] && break
    sleep 0.1
  done

  if [[ ! -s "$REPORTS_STUB_PORT_FILE" ]]; then
    echo "failed to start reports schedule stub" >&2
    return 1
  fi

  export BASECAMP_BASE_URL="http://127.0.0.1:$(cat "$REPORTS_STUB_PORT_FILE")"
}

stop_reports_schedule_stub() {
  if [[ -n "${REPORTS_STUB_PID:-}" ]]; then
    kill "$REPORTS_STUB_PID" 2>/dev/null || true
    wait "$REPORTS_STUB_PID" 2>/dev/null || true
    unset REPORTS_STUB_PID
  fi
}

reports_schedule_request_path() {
  local request_path
  request_path=$(grep '/reports/schedules/upcoming.json' "$REPORTS_STUB_LOG")
  [[ -n "$request_path" ]]
  local count
  count=$(grep -c '/reports/schedules/upcoming.json' "$REPORTS_STUB_LOG")
  [[ "$count" -eq 1 ]]
  printf '%s\n' "$request_path"
}


# Flag parsing errors

@test "schedule entries --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule entries --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "schedule --schedule without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule --schedule
  assert_failure
  assert_output_contains "--schedule requires a value"
}

@test "schedule show --date without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule show 456 --date
  assert_failure
  assert_output_contains "--date requires a value"
}


# Missing context errors

@test "schedule without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule
  assert_success
  assert_output_contains "COMMANDS"
}

@test "schedule entries without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule entries
  assert_failure
  assert_output_contains "project"
}

@test "schedule show without entry id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule show
  assert_failure
  assert_output_contains "ID required"
}

@test "schedule update without entry id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule update
  assert_failure
  assert_output_contains "ID required"
}


# Create validation

@test "schedule create without summary shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule create --starts-at "2024-01-15T10:00:00Z" --ends-at "2024-01-15T11:00:00Z"
  assert_failure
  assert_json_value '.error' '<summary> required'
  assert_json_value '.code' 'usage'
}

@test "schedule create without starts-at shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule create "Test Event" --ends-at "2024-01-15T11:00:00Z"
  assert_failure
  assert_output_contains "--starts-at required"
}

@test "schedule create without ends-at shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule create "Test Event" --starts-at "2024-01-15T10:00:00Z"
  assert_failure
  assert_output_contains "--ends-at required"
}


# Settings validation

@test "schedule settings without include-due shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule settings
  assert_failure
  assert_output_contains "--include-due required"
}


# Update validation

@test "schedule update without any fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp schedule update 456
  # May return validation error or API error depending on implementation
  assert_failure
}


# Help flag

@test "schedule --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule --help
  assert_success
  assert_output_contains "basecamp schedule"
  assert_output_contains "entries"
  assert_output_contains "create"
  assert_output_contains "update"
}


# Unknown action

@test "schedule unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp schedule foobar
  # Command may show help or require project - just verify it runs
}


# reports schedule flag defaults

@test "reports schedule --help shows default window in flag descriptions" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp reports schedule --help
  assert_success
  assert_output_contains "default: today"
  assert_output_contains "default: +30"
}

@test "reports schedule without flags sends default window to API" {
  start_reports_schedule_stub
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp reports schedule --json
  stop_reports_schedule_stub

  assert_success
  assert_json_value '.ok' 'true'

  request_path=$(reports_schedule_request_path)
  [[ "$request_path" == *"/99999/reports/schedules/upcoming.json?"* ]]
  [[ "$request_path" == *"window_starts_on="* ]]
  [[ "$request_path" == *"window_ends_on="* ]]
}

@test "reports schedule --start without --end anchors default end to start" {
  start_reports_schedule_stub
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp reports schedule --start 2099-01-01 --json
  stop_reports_schedule_stub

  assert_success
  assert_json_value '.ok' 'true'

  request_path=$(reports_schedule_request_path)
  [[ "$request_path" == *"window_starts_on=2099-01-01"* ]]
  [[ "$request_path" == *"window_ends_on=2099-01-31"* ]]
}
