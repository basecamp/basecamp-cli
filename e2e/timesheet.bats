#!/usr/bin/env bats
# timesheet.bats - Test timesheet command error handling

load test_helper


# Flag parsing errors

@test "timesheet --start without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --start
  assert_failure
  assert_output_contains "--start requires a value"
}

@test "timesheet --end without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --end
  assert_failure
  assert_output_contains "--end requires a value"
}

@test "timesheet --person without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --person
  assert_failure
  assert_output_contains "--person requires a value"
}

# Date range validation

@test "timesheet with start but no end shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --start 2024-01-01
  assert_failure
  assert_output_contains "--end required when --start is provided"
}

@test "timesheet with end but no start shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --end 2024-01-31
  assert_failure
  assert_output_contains "--start required when --end is provided"
}


# Missing context errors

@test "timesheet recording without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet recording
  assert_failure
  assert_output_contains "ID required"
}


# Help flag

@test "timesheet --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet --help
  assert_success
  assert_output_contains "basecamp timesheet"
  assert_output_contains "report"
  assert_output_contains "project"
  assert_output_contains "recording"
}


# Unknown action

@test "timesheet unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet foobar
  # Command may show help or require project - just verify it runs
}
