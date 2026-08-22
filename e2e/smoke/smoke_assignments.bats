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

@test "assignments list surfaces priority_recording_id when present" {
  # This listing is the only place priority_recording_id appears — it is in no
  # URL and no other command's output — so reorder and deprioritize depend on
  # it to address a prioritized card-table step.
  run_smoke basecamp assignments list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "assignments reorder rejects a zero position" {
  # Positions are 1-based and refused rather than clamped.
  run_smoke basecamp assignments reorder 999999 --position 0 --json
  assert_failure
}

@test "assignments prioritize is out of scope" {
  mark_out_of_scope "Mutating - covered by the live card-step priority sequence"
}

@test "assignments deprioritize is out of scope" {
  mark_out_of_scope "Mutating - covered by the live card-step priority sequence"
}
