#!/usr/bin/env bats
# hillcharts.bats - Test hillcharts command error handling

load test_helper


# Bare command shows help

@test "hillcharts without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts
  assert_success
  assert_output_contains "COMMANDS"
}


# Missing project errors

@test "hillcharts show without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts show
  assert_failure
  assert_output_contains "project"
}

@test "hillcharts show with --todoset but no project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts show --todoset 12345
  assert_failure
  assert_output_contains "project"
}

@test "hillcharts track without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts track
  assert_failure
}

@test "hillcharts untrack without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts untrack
  assert_failure
}


# Help

@test "hillcharts --help shows help" {
  create_credentials
  create_global_config '{}'

  run basecamp hillcharts --help
  assert_success
  assert_output_contains "basecamp hillcharts"
  assert_output_contains "hill chart"
  assert_output_contains "--project"
}
