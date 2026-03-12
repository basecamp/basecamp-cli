#!/usr/bin/env bats
# qa_happypath.bats - Minimal happy-path tests replaying committed cassettes.
# Catches the "files list was busted" class of bug before merge.
#
# Scope: deterministic read-only commands replayed from a single cassette.
# This verifies the CLI correctly parses API responses and produces valid
# JSON output — not that the recorder handles arbitrary interaction patterns.
#
# Recording: make record-cassettes (requires TOKEN, TARGET, ACCOUNT, PROJECT)
# Replaying: make test-e2e  (automatic — cassettes committed to repo)

load test_helper

CASSETTE_DIR="$BATS_TEST_DIRNAME/cassettes/happypath"

setup_file() {
  if [[ -n "${BASECAMP_RECORD_TOKEN:-}" ]]; then
    # Record mode: forward through proxy to live server, save cassettes
    if [[ -z "${BASECAMP_RECORD_TARGET:-}" ]]; then
      echo "ERROR: BASECAMP_RECORD_TARGET required in record mode" >&2
      return 1
    fi
    start_proxy record "$CASSETTE_DIR" "$BASECAMP_RECORD_TARGET"
  else
    # Replay mode: serve from committed cassettes
    if [[ ! -d "$CASSETTE_DIR" ]] || ! ls "$CASSETTE_DIR"/*.json &>/dev/null; then
      if [[ -n "${QA_HAPPYPATH_OPTIONAL:-}" ]]; then
        skip "No cassettes found. Run 'make record-cassettes' first."
      fi
      echo "ERROR: No cassettes in $CASSETTE_DIR" >&2
      echo "Record them with: make record-cassettes" >&2
      echo "Or set QA_HAPPYPATH_OPTIONAL=1 to skip during bring-up." >&2
      return 1
    fi
    start_proxy replay "$CASSETTE_DIR"
  fi
}

teardown_file() {
  stop_proxy
}

# Per-test setup: point CLI at the proxy, configure credentials.
# Both record and replay use BASECAMP_TOKEN (bypasses OAuth/account lookup).
# Account ID and project ID must match the data baked into the cassettes.
setup_extra() {
  local acct="${BASECAMP_RECORD_ACCOUNT:-181900405}"
  local proj="${BASECAMP_RECORD_PROJECT:-2085958494}"

  export BASECAMP_BASE_URL="http://127.0.0.1:${REPLAY_PORT}"
  export BASECAMP_ACCOUNT_ID="$acct"
  export BASECAMP_PROJECT_ID="$proj"

  if [[ -n "${BASECAMP_RECORD_TOKEN:-}" ]]; then
    export BASECAMP_TOKEN="$BASECAMP_RECORD_TOKEN"
  else
    export BASECAMP_TOKEN="test-token"
  fi
}


# --- Happy-path tests ---
# Each test asserts .ok, output shape, and that domain-specific text
# (e.g. "projects", "todos") appears in the output — verifying the CLI
# parsed the API response, not just that it exited 0.
#
# Note: accounts list is excluded — it hits /authorization.json which lives
# on Launchpad, not the Basecamp API, so it can't be proxied single-host.

@test "projects list returns projects" {
  run basecamp projects list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data[0].id'
  assert_json_not_null '.data[0].name'
  assert_output_contains 'projects'   # summary includes "N projects"
}

@test "todos list returns todos" {
  run basecamp todos list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '(.data | type)' 'array'
  assert_output_contains 'todos'      # summary includes "N todos"
}

@test "files list returns files" {
  run basecamp files list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.summary'
  assert_output_contains 'files'      # summary includes "N folders, N files, N documents"
}

@test "recordings list returns recordings" {
  run basecamp recordings list --type Todo --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '(.data | type)' 'array'
  assert_output_contains 'Todos'      # summary includes "N Todos"
}

@test "messages list returns messages" {
  run basecamp messages list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '(.data | type)' 'array'
  assert_output_contains 'messages'   # summary includes "N messages"
}
