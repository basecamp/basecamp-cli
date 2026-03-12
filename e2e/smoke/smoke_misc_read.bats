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
  assert_json_not_null '.data.id'
}

@test "schedule info returns schedule" {
  ensure_project || mark_unverifiable "Cannot discover project for schedule"
  ensure_schedule || mark_unverifiable "Cannot discover schedule"
  run_smoke basecamp schedule info --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
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
  assert_success
  assert_json_value '.ok' 'true'
}

@test "schedule entries returns entries" {
  ensure_project || mark_unverifiable "Cannot discover project for schedule entries"
  ensure_schedule || mark_unverifiable "Cannot discover schedule"
  run_smoke basecamp schedule entries --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
