#!/usr/bin/env bats
# smoke_tools.bats - Level 0/1: Project tools (dock items)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "tools show returns a tool" {
  # Use the message board dock item as a known tool.
  # ensure_messageboard calls mark_unverifiable (writes trace + sets
  # BATS_TEST_SKIPPED) before returning 1, so this shows as "skipped".
  ensure_messageboard || return 0
  run_smoke basecamp tools show "$QA_MESSAGEBOARD" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
