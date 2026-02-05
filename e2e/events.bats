#!/usr/bin/env bats
# events.bats - Test events command error handling

load test_helper


# Missing context errors

@test "events without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp events
  assert_failure
  assert_output_contains "ID required"
}

# Help

@test "events --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp events --help
  assert_success
  assert_output_contains "basecamp events"
  assert_output_contains "recording_id"
  assert_output_contains "audit"
}
