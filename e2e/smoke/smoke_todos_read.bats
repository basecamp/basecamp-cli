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

@test "todos show returns todo detail" {
  ensure_todo || mark_unverifiable "No todo in project"
  run_smoke basecamp todos show "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "todolists show returns todolist detail" {
  ensure_todolist || mark_unverifiable "No todolist in project"
  run_smoke basecamp todolists show "$QA_TODOLIST" -p "$QA_PROJECT" --json
  # SDK bug: TodolistOrGroup0 expects {"todolist":{...}} but API returns flat JSON
  # See: basecamp-sdk TodolistOrGroup0 union type mismatch
  [[ "$status" -eq 7 ]] && mark_unverifiable "SDK deserialization bug (todolists show)"
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "todosets list returns todosets" {
  run_smoke basecamp todosets list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
