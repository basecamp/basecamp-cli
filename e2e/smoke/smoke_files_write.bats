#!/usr/bin/env bats
# smoke_files_write.bats - Level 1: File upload and folder operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "files folders create creates a vault" {
  run_smoke basecamp files folders create "Smoke vault $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
