#!/usr/bin/env bats
# smoke_todos_read.bats - Level 0: Todo read operations
#
# No ensure_todolist in setup — todolists list IS the test.

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "todosets show returns todoset" {
  run_smoke basecamp todosets show -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todolists list returns todolists" {
  run_smoke basecamp todolists list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos list returns todos" {
  run_smoke basecamp todos list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
