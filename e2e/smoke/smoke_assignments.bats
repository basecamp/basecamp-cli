#!/usr/bin/env bats
# smoke_assignments.bats - Level 0: My assignments operations

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "assignments list returns assignments" {
  run_smoke basecamp assignments list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "assignments completed returns completed items" {
  run_smoke basecamp assignments completed --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "assignments due returns due items" {
  run_smoke basecamp assignments due overdue --json
  assert_success
  assert_json_value '.ok' 'true'
}
