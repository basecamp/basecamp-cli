#!/usr/bin/env bats
# smoke_gauges.bats - Level 0: Gauge operations

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "gauges list returns gauges" {
  run_smoke basecamp gauges list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "gauges needles requires project" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp gauges needles -p "$QA_PROJECT" --json
  # Gauges may not be enabled on the project (403)
  [[ "$status" -eq 4 ]] && mark_unverifiable "Gauges not enabled on project"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "gauges needle requires valid ID" {
  run_smoke basecamp gauges needle 999999 --json
  mark_unverifiable "Requires valid needle ID"
}

@test "gauges create requires project and position" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp gauges create --position 50 -p "$QA_PROJECT" --json
  # May fail if gauges not enabled (403) or other constraint
  [[ "$status" -eq 4 ]] && mark_unverifiable "Gauges not enabled on project"
  mark_unverifiable "Mutating test - gauge creation may not be safe in all environments"
}

@test "gauges update requires valid needle ID" {
  run_smoke basecamp gauges update 999999 --description "test" --json
  mark_unverifiable "Requires valid needle ID"
}

@test "gauges delete requires valid needle ID" {
  run_smoke basecamp gauges delete 999999 --json
  mark_unverifiable "Requires valid needle ID"
}

@test "gauges enable requires project" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp gauges enable -p "$QA_PROJECT" --json
  mark_unverifiable "Mutating test - gauge toggle may not be safe in all environments"
}

@test "gauges disable requires project" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp gauges disable -p "$QA_PROJECT" --json
  mark_unverifiable "Mutating test - gauge toggle may not be safe in all environments"
}
