#!/usr/bin/env bats
# smoke_files_read.bats - Level 0: Files, vaults, docs, uploads (read-only)
#
# No ensure_vault — vaults list IS the test.

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "files list returns files" {
  run_smoke basecamp files list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "vaults list returns vaults" {
  run_smoke basecamp vaults list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "docs list returns documents" {
  run_smoke basecamp docs list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "uploads list returns uploads" {
  run_smoke basecamp uploads list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
