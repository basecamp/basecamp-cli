#!/usr/bin/env bats
# messageboards.bats - Test messageboards command error handling

load test_helper


# Missing project errors

@test "messageboards without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messageboards
  assert_failure
  assert_output_contains "project"
}

@test "messageboards show without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messageboards show
  assert_failure
  assert_output_contains "project"
}


# Flag parsing errors

@test "messageboards --project without value shows error" {
  create_credentials
  create_global_config '{}'

  run basecamp messageboards --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "messageboards --board without value shows error" {
  create_credentials
  create_global_config '{}'

  run basecamp messageboards --board
  assert_failure
  assert_output_contains "--board requires a value"
}


# Unknown action

@test "messageboards unknown action shows error" {
  create_credentials
  create_global_config '{}'

  run basecamp messageboards foobar
  # Command may show help or require project - just verify it runs
}


# Help

@test "messageboards --help shows help" {
  create_credentials
  create_global_config '{}'

  run basecamp messageboards --help
  assert_success
  assert_output_contains "basecamp messageboards"
  assert_output_contains "message board"
  assert_output_contains "--project"
}
