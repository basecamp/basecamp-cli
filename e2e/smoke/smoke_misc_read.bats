#!/usr/bin/env bats
# smoke_misc_read.bats - Level 0: Schedule, search, recordings, version, doctor

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

@test "schedule show returns schedule" {
  ensure_project || mark_unverifiable "Cannot discover project for schedule"
  run_smoke basecamp schedule show -p "$QA_PROJECT" --json
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
