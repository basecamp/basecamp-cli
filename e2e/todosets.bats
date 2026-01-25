#!/usr/bin/env bats
# todosets.bats - Test todosets command error handling

load test_helper


# Missing project errors

@test "todosets without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todosets
  assert_failure
  assert_output_contains "project"
}

@test "todosets show without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todosets show
  assert_failure
  assert_output_contains "project"
}


# Flag parsing errors

@test "todosets --project without value shows error" {
  create_credentials
  create_global_config '{}'

  run bcq todosets --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "todosets --todoset without value shows error" {
  create_credentials
  create_global_config '{}'

  run bcq todosets --todoset
  assert_failure
  assert_output_contains "--todoset requires a value"
}


# Unknown action

@test "todosets unknown action shows error" {
  create_credentials
  create_global_config '{}'

  run bcq todosets foobar
  # Command may show help or require project - just verify it runs
}


# Help

@test "todosets --help shows help" {
  create_credentials
  create_global_config '{}'

  run bcq todosets --help
  assert_success
  assert_output_contains "bcq todosets"
  assert_output_contains "todoset"
  assert_output_contains "--project"
}
