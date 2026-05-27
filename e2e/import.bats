#!/usr/bin/env bats
# import.bats - deterministic CSV import artifact flow

load test_helper

teardown_extra() {
  stop_import_mock || true
}

start_import_mock() {
  IMPORT_MOCK_DIR="$(mktemp -d)"
  IMPORT_MOCK_PORT_FILE="$IMPORT_MOCK_DIR/port"
  IMPORT_MOCK_REQUEST_LOG="$IMPORT_MOCK_DIR/requests.jsonl"
  export IMPORT_MOCK_REQUEST_LOG

  cat > "$IMPORT_MOCK_DIR/server.py" <<'PY'
import json
import os
import socketserver
from http.server import BaseHTTPRequestHandler

request_log = os.environ["IMPORT_MOCK_REQUEST_LOG"]
fail_todo_title = os.environ.get("IMPORT_MOCK_FAIL_TODO_TITLE", "")
list_ids = {"Home": 901, "Events": 902}
next_todo_id = 1000

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        return

    def _read_body(self):
        length = int(self.headers.get("content-length", "0"))
        raw = self.rfile.read(length) if length else b""
        if not raw:
            return None, ""
        text = raw.decode("utf-8")
        try:
            return json.loads(text), text
        except json.JSONDecodeError:
            return None, text

    def _write_json(self, status, payload):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _record(self, body):
        with open(request_log, "a", encoding="utf-8") as f:
            f.write(json.dumps({"method": self.command, "path": self.path, "body": body}) + "\n")

    def do_GET(self):
        self._record(None)
        if self.path == "/99999/projects/12345.json":
            self._write_json(200, {
                "id": 12345,
                "name": "Import Project",
                "dock": [{"id": 777, "name": "todoset", "title": "To-dos", "enabled": True}],
            })
            return
        if self.path == "/99999/todosets/777/todolists.json":
            self._write_json(200, [])
            return
        if self.path.startswith("/99999/todolists/") and self.path.endswith("/todos.json"):
            self._write_json(200, [])
            return
        self._write_json(500, {"error": "unexpected GET", "path": self.path})

    def do_POST(self):
        global next_todo_id
        body, raw = self._read_body()
        self._record(body if body is not None else raw)
        if self.path == "/99999/todosets/777/todolists.json":
            name = (body or {}).get("name", "")
            todolist_id = list_ids.get(name, 999)
            self._write_json(201, {
                "id": todolist_id,
                "status": "active",
                "title": name,
                "name": name,
                "type": "Todolist",
                "url": f"https://3.basecampapi.com/99999/todolists/{todolist_id}.json",
                "app_url": f"https://3.basecamp.com/99999/buckets/12345/todolists/{todolist_id}",
                "inherits_status": True,
                "visible_to_clients": False,
                "bucket": {"id": 12345, "name": "Import Project", "type": "Project"},
                "parent": {"id": 777, "title": "To-dos", "type": "Todoset", "url": "", "app_url": ""},
                "creator": {"id": 1, "name": "Tester"}
            })
            return
        if self.path.startswith("/99999/todolists/") and self.path.endswith("/todos.json"):
            content = (body or {}).get("content", "")
            if fail_todo_title and content == fail_todo_title:
                self._write_json(500, {"error": "configured todo failure", "title": content})
                return
            next_todo_id += 1
            self._write_json(201, {
                "id": next_todo_id,
                "status": "active",
                "content": content,
                "title": content,
                "description": (body or {}).get("description", ""),
                "due_on": (body or {}).get("due_on", ""),
                "type": "Todo",
                "url": f"https://3.basecampapi.com/99999/todos/{next_todo_id}.json",
                "app_url": f"https://3.basecamp.com/99999/todos/{next_todo_id}",
                "inherits_status": True,
                "visible_to_clients": False,
                "bucket": {"id": 12345, "name": "Import Project", "type": "Project"},
                "parent": {"id": 901, "title": "List", "type": "Todolist", "url": "", "app_url": ""},
                "creator": {"id": 1, "name": "Tester"}
            })
            return
        self._write_json(500, {"error": "unexpected POST", "path": self.path})

with socketserver.TCPServer(("127.0.0.1", 0), Handler) as httpd:
    with open(os.environ["IMPORT_MOCK_PORT_FILE"], "w", encoding="utf-8") as f:
        f.write(str(httpd.server_address[1]))
    httpd.serve_forever()
PY

  IMPORT_MOCK_PORT_FILE="$IMPORT_MOCK_PORT_FILE" python3 "$IMPORT_MOCK_DIR/server.py" >"$IMPORT_MOCK_DIR/server.log" 2>&1 &
  IMPORT_MOCK_PID=$!

  local i=0
  while [[ ! -s "$IMPORT_MOCK_PORT_FILE" ]] && (( i < 50 )); do
    sleep 0.1
    i=$((i + 1))
  done
  if [[ ! -s "$IMPORT_MOCK_PORT_FILE" ]]; then
    cat "$IMPORT_MOCK_DIR/server.log" >&2 || true
    return 1
  fi
  IMPORT_MOCK_PORT="$(<"$IMPORT_MOCK_PORT_FILE")"
  export IMPORT_MOCK_PORT IMPORT_MOCK_PID IMPORT_MOCK_DIR
}

stop_import_mock() {
  if [[ -n "${IMPORT_MOCK_PID:-}" ]]; then
    kill "$IMPORT_MOCK_PID" 2>/dev/null || true
    wait "$IMPORT_MOCK_PID" 2>/dev/null || true
    unset IMPORT_MOCK_PID
  fi
}

write_import_csv() {
  cat > tasks.csv <<'CSV'
id,title,notes,list,status,owner,due,link,priority,note,note
T-1,Buy paint,"Get blue, low VOC",Home,todo,alex@example.com,2026-06-01,https://example.com/a,High,"Bring old card","Ask for sample"
T-2,Book venue,Call two places,Events,doing,jamie@example.com,2026-06-03,https://example.com/b,Low,,"Confirm deposit"
CSV
}

write_mapping_json() {
  cat > mapping.json <<'JSON'
{
  "schema_version": 1,
  "record_id": { "column_index": 0, "column_name": "id" },
  "title": { "column_index": 1, "column_name": "title" },
  "description": { "column_index": 2, "column_name": "notes" },
  "todolist": { "column_index": 3, "column_name": "list" },
  "status": { "column_index": 4, "column_name": "status" },
  "assignees": { "column_index": 5, "column_name": "owner" },
  "due_on": { "column_index": 6, "column_name": "due" },
  "attachment_urls": [{ "column_index": 7, "column_name": "link" }],
  "custom_fields": "all_unmapped_columns"
}
JSON
}

write_destination_json() {
  cat > destination.json <<'JSON'
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "create_from_column"
}
JSON
}

compile_import_artifact() {
  write_import_csv
  write_mapping_json
  write_destination_json

  run basecamp import inspect tasks.csv --json --sample-size 2
  assert_success
  echo "$output" > inspection.json

  run basecamp import compile --inspection inspection.json --mapping mapping.json --destination destination.json --out basecamp-import --json
  assert_success
}

@test "import inspect profiles CSV facts without writes" {
  write_import_csv

  run basecamp import inspect tasks.csv --json --sample-size 2
  assert_success
  is_valid_json
  assert_json_value ".ok" "true"
  assert_json_value ".data.status" "profiled"
  assert_json_value ".data.row_count" "2"
  assert_json_value ".data.columns[1].name" "title"
  assert_json_value ".data.duplicate_headers[0].name" "note"
  assert_json_not_null ".data.fingerprint.value"
  assert_json_value ".data.role_candidates.title[0].column_index" "1"
}

@test "import compile writes validated artifact and plan reads it" {
  compile_import_artifact

  is_valid_json
  assert_json_value ".data.status" "compiled"
  assert_json_value ".data.manifest.artifact_format" "basecamp-import-csv-v1"
  assert_json_value ".data.manifest.counts.todos" "2"
  assert_json_value ".data.manifest.counts.todolists" "2"

  [[ -f basecamp-import/import.json ]]
  [[ -f basecamp-import/todos.csv ]]
  head -1 basecamp-import/todos.csv | grep -q "source_path,source_row,source_record_id"
  grep -q "custom_fields_json" basecamp-import/todos.csv
  grep -q '""priority"":""High""' basecamp-import/todos.csv

  run basecamp import plan --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "ready_for_approval"
  assert_json_value ".data.counts.todos" "2"
  assert_json_value ".data.counts.todolists" "2"
  assert_output_contains "Row 1: create todo \\\"Buy paint\\\""

  run basecamp import status --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "not_executed"
  assert_json_value ".data.counts.todos" "2"

  run basecamp import repair --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "not_executed"

  run basecamp import followup --artifact basecamp-import --out followup-import --reviewed --json
  assert_failure
  assert_output_contains "review_required"
}

@test "import compile rejects a CSV changed after inspection" {
  write_import_csv
  write_mapping_json
  write_destination_json

  run basecamp import inspect tasks.csv --json
  assert_success
  echo "$output" > inspection.json

  cat >> tasks.csv <<'CSV'
T-3,Changed after inspect,This row invalidates fingerprint,Later,todo,,2026-06-04,,Medium,,
CSV

  run basecamp import compile --inspection inspection.json --mapping mapping.json --destination destination.json --out basecamp-import --json
  assert_failure
  assert_output_contains "fingerprint changed"
}

@test "import plan asks for assignee policy when source has display names" {
  cat > tasks.csv <<'CSV'
id,title,owner
1,Call vendor,Alex Rivera
CSV
  cat > mapping.json <<'JSON'
{
  "schema_version": 1,
  "title": { "column_index": 1 },
  "assignees": { "column_index": 2 }
}
JSON
  cat > destination.json <<'JSON'
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "single_todolist",
  "todolist_name": "Imported todos"
}
JSON

  run basecamp import inspect tasks.csv --json
  assert_success
  echo "$output" > inspection.json

  run basecamp import plan --inspection inspection.json --mapping mapping.json --destination destination.json --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "requires_user_input"
  assert_json_value ".data.requires_user_input" "true"
  assert_output_contains "confirm_assignee_policy"
}

@test "import execute requires explicit approval before account or network work" {
  compile_import_artifact

  run basecamp import execute --artifact basecamp-import --json
  assert_failure
  assert_output_contains "--approved required"
}

configure_import_mock_basecamp() {
  export BASECAMP_BASE_URL="http://127.0.0.1:${IMPORT_MOCK_PORT}"
  export BASECAMP_ACCOUNT_ID="99999"
  export BASECAMP_TOKEN="test-token"
}

@test "import execute creates todolists and todos against replay server" {
  compile_import_artifact
  start_import_mock

  configure_import_mock_basecamp

  run basecamp import preflight --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "passed"

  run basecamp import execute --artifact basecamp-import --approved --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "completed"
  assert_json_value ".data.created.todolists" "2"
  assert_json_value ".data.created.todos" "2"

  jq -e 'select(.method == "POST" and .path == "/99999/todosets/777/todolists.json" and .body.name == "Home")' "$IMPORT_MOCK_REQUEST_LOG" >/dev/null
  jq -e 'select(.method == "POST" and .path == "/99999/todosets/777/todolists.json" and .body.name == "Events")' "$IMPORT_MOCK_REQUEST_LOG" >/dev/null
  jq -e 'select(.method == "POST" and .path == "/99999/todolists/901/todos.json" and .body.content == "Buy paint" and .body.due_on == "2026-06-01" and (.body.description | contains("Get blue, low VOC")))' "$IMPORT_MOCK_REQUEST_LOG" >/dev/null
  jq -e 'select(.method == "POST" and .path == "/99999/todolists/902/todos.json" and .body.content == "Book venue" and .body.due_on == "2026-06-03" and (.body.description | contains("Confirm deposit")))' "$IMPORT_MOCK_REQUEST_LOG" >/dev/null

  [[ -f basecamp-import/execution.json ]]
  [[ "$(jq -r '.status' basecamp-import/execution.json)" == "completed" ]]

  run basecamp import status --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "completed"
  assert_json_value ".data.execution.created.todos" "2"
  assert_json_value ".data.execution.operations[0].op" "create_todolist"
  assert_json_value ".data.execution.operations[2].source_row" "1"

  run basecamp import repair --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "completed"
  assert_json_value ".data.completed_operations[2].source_row" "1"

  local request_count
  request_count="$(wc -l < "$IMPORT_MOCK_REQUEST_LOG")"

  run basecamp import execute --artifact basecamp-import --approved --json
  assert_failure
  assert_output_contains "execution refuses to run again"
  [[ "$(wc -l < "$IMPORT_MOCK_REQUEST_LOG")" == "$request_count" ]]
}

@test "import followup creates pending-row artifact after failed execution" {
  compile_import_artifact
  export IMPORT_MOCK_FAIL_TODO_TITLE="Book venue"
  start_import_mock
  unset IMPORT_MOCK_FAIL_TODO_TITLE

  configure_import_mock_basecamp

  run basecamp import execute --artifact basecamp-import --approved --json
  assert_failure
  assert_output_contains "configured todo failure"

  run basecamp import status --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "failed"
  assert_json_value ".data.execution.created.todos" "1"
  assert_json_value ".data.execution.operations[-1].status" "failed"

  run basecamp import repair --artifact basecamp-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "review_required"
  assert_json_value ".data.pending_todos[0].source_row" "2"
  assert_json_value ".data.pending_todos[0].title" "Book venue"

  run basecamp import followup --artifact basecamp-import --out followup-import --reviewed --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "compiled"
  assert_json_value ".data.manifest.counts.todos" "1"
  assert_json_value ".data.pending_todos[0].source_row" "2"

  run basecamp import plan --artifact followup-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "ready_for_approval"
  assert_json_value ".data.counts.todos" "1"
  assert_json_value ".data.counts.todolists" "0"
  assert_json_value ".data.operations[0].source_row" "2"
  assert_json_value ".data.operations[0].title" "Book venue"

  run basecamp import preflight --artifact followup-import --json
  assert_success
  is_valid_json
  assert_json_value ".data.status" "passed"
}
