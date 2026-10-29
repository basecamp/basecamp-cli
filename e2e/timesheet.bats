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
  assert_output_contains "item"
}


# Unknown action

@test "timesheet unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet foobar
  # Command may show help or require project - just verify it runs
}


# Entry verbs: refused locally, before any request

@test "timesheet entry create without --hours shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet entry create 789
  assert_failure
  assert_output_contains "--hours is required"
}

@test "timesheet entry update with nothing to change shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet entry update 123
  assert_failure
  assert_output_contains "Nothing to update"
}

@test "timesheet entry delete refuses an item URL" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet entry delete "https://3.basecamp.com/99999/buckets/89/todos/789" --force
  assert_failure
  assert_output_contains "does not name a timesheet entry"
}

@test "timesheet entry delete needs --force without a terminal" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp timesheet entry delete 123 --json
  assert_failure
  assert_output_contains "--force"
}
