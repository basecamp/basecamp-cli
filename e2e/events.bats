#!/usr/bin/env bats
# events.bats - Test events command error handling

load test_helper


# Missing context errors

@test "events without recording id shows the group's help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp events
  assert_success
  assert_output_contains "poll"
  assert_output_contains "ticket"
}

@test "events with a non-numeric recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp events not-an-id
  assert_failure
  assert_output_contains "Invalid ID"
}

# Help

@test "events --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp events --help
  assert_success
  assert_output_contains "basecamp events"
  assert_output_contains "id|url"
  assert_output_contains "audit"
}
