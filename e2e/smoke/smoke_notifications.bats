#!/usr/bin/env bats
# smoke_notifications.bats - Level 0: Notification operations

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "notifications list returns notifications" {
  run_smoke basecamp notifications list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "notifications read rejects unknown ID" {
  run_smoke basecamp notifications read 999999 --json
  assert_failure
  assert_output_contains "not found"
}

@test "notifications bubbleups lists bubble ups" {
  run_smoke basecamp notifications bubbleups --json
  # Bubble Ups are a BC5-added endpoint; may not exist on all QA environments
  [[ "$status" -ne 0 ]] && mark_unverifiable "Bubble Ups endpoint not available"
  assert_json_value '.ok' 'true'
}
