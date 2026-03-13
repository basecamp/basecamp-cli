#!/usr/bin/env bats
# smoke_misc_read.bats - Level 0: Read-only misc commands

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "me returns current user" {
  run_smoke basecamp me --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.identity.id'
}

@test "recordings list returns recordings" {
  run_smoke basecamp recordings list --type Todo --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "search returns results" {
  run_smoke basecamp search "test" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "search metadata returns metadata" {
  run_smoke basecamp search metadata --json
  # Search metadata requires projects with search enabled
  if [[ "$status" -ne 0 ]]; then
    mark_unverifiable "Search metadata not available"
    return 0
  fi
  assert_json_value '.ok' 'true'
}
