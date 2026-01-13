#!/usr/bin/env bats
# events.bats - Test events command error handling

load test_helper


# Missing context errors

@test "events without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq events
  assert_failure
  assert_output_contains "Recording ID required"
}

@test "events without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq events 12345
  assert_failure
  assert_output_contains "No project specified"
}


# Flag parsing

@test "events --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq events 12345 --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Help

@test "events --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq events --help
  assert_success
  assert_output_contains "bcq events"
  assert_output_contains "recording_id"
  assert_output_contains "audit"
}
