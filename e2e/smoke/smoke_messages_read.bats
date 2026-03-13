#!/usr/bin/env bats
# smoke_messages_read.bats - Level 0: Messages and message boards (read-only)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "messages list returns messages" {
  run_smoke basecamp messages list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messageboards show returns message board" {
  run_smoke basecamp messageboards show -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages show returns message detail" {
  ensure_message || mark_unverifiable "No message in project"
  run_smoke basecamp messages show "$QA_MESSAGE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
