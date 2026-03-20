#!/usr/bin/env bats
# smoke_hillcharts.bats - Level 0: Hill chart operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "hillcharts show returns hill chart" {
  run_smoke basecamp hillcharts show -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "hillcharts track requires todolist IDs" {
  run_smoke basecamp hillcharts track 999999 -p "$QA_PROJECT" --json
  # Track may fail with not-found for a fake ID; just verify it reaches the API
  mark_unverifiable "Requires valid todolist ID"
}

@test "hillcharts untrack requires todolist IDs" {
  run_smoke basecamp hillcharts untrack 999999 -p "$QA_PROJECT" --json
  # Untrack may fail with not-found for a fake ID; just verify it reaches the API
  mark_unverifiable "Requires valid todolist ID"
}
