#!/usr/bin/env bats
# messageboards.bats - Test messageboards command error handling

load test_helper


# Missing project errors

@test "messageboards without project shows error" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards
  assert_failure
  assert_output_contains "No project specified"
}

@test "messageboards show without project shows error" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards show
  assert_failure
  assert_output_contains "No project specified"
}


# Flag parsing errors

@test "messageboards --project without value shows error" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "messageboards --board without value shows error" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards --board
  assert_failure
  assert_output_contains "--board requires a value"
}


# Unknown action

@test "messageboards unknown action shows error" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards foobar
  assert_failure
  assert_output_contains "Unknown messageboards action"
}


# Help

@test "messageboards --help shows help" {
  create_credentials
  create_global_config '{}'

  run bcq messageboards --help
  assert_success
  assert_output_contains "bcq messageboards"
  assert_output_contains "message board"
  assert_output_contains "--project"
}
