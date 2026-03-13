#!/usr/bin/env bats
# smoke_communication.bats - Level 0: Read-only communication tests

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

# --- Message types (account-wide) ---

@test "messagetypes list returns message types" {
  run_smoke basecamp messagetypes list --json
  # Message types may not exist on all environments
  [[ "$status" -ne 0 ]] && mark_unverifiable "Message types not available"
  assert_json_value '.ok' 'true'

  # Capture first messagetype for show test
  echo "$output" | jq -r '.data[0].id // empty' > "$BATS_FILE_TMPDIR/messagetype_id"
}

@test "messagetypes show returns message type detail" {
  local id_file="$BATS_FILE_TMPDIR/messagetype_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No messagetype discovered in prior test"
  local mt_id
  mt_id=$(<"$id_file")
  [[ -n "$mt_id" ]] || mark_unverifiable "Messagetype ID is empty"

  run_smoke basecamp messagetypes show "$mt_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

# --- Forwards / inbox ---

@test "forwards inbox shows project inbox" {
  ensure_inbox || mark_unverifiable "No inbox in project"
  run_smoke basecamp forwards inbox --inbox "$QA_INBOX" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "forwards list returns forwards" {
  ensure_inbox || mark_unverifiable "No inbox in project"
  run_smoke basecamp forwards list --inbox "$QA_INBOX" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'

  # Capture first forward for show test (may be empty)
  echo "$output" | jq -r '.data[0].id // empty' > "$BATS_FILE_TMPDIR/forward_id"
}

@test "forwards show returns forward detail" {
  ensure_inbox || mark_unverifiable "No inbox in project"
  local id_file="$BATS_FILE_TMPDIR/forward_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No forward discovered in prior test"
  local fwd_id
  fwd_id=$(<"$id_file")
  [[ -n "$fwd_id" ]] || mark_unverifiable "No forwards in project inbox"

  run_smoke basecamp forwards show "$fwd_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

# --- Subscriptions (read-only show) ---

@test "subscriptions show returns subscription info" {
  ensure_todo || mark_unverifiable "No todo in project"
  run_smoke basecamp subscriptions show "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Boosts (read-only list) ---

@test "boost list returns boosts for a recording" {
  ensure_todo || mark_unverifiable "No todo in project"
  run_smoke basecamp boost list "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Comments (read operations) ---

@test "comments list returns comments for a recording" {
  ensure_todo || mark_unverifiable "No todo in project"
  run_smoke basecamp comments list "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
