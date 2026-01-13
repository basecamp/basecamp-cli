#!/usr/bin/env bats
# schedule.bats - Test schedule command error handling

load test_helper


# Flag parsing errors

@test "schedule entries --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule entries --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "schedule --schedule without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule --schedule
  assert_failure
  assert_output_contains "--schedule requires a value"
}


# Missing context errors

@test "schedule without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule
  assert_failure
  assert_output_contains "No project specified"
}

@test "schedule entries without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule entries
  assert_failure
  assert_output_contains "No project specified"
}

@test "schedule show without entry id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule show
  assert_failure
  assert_output_contains "Entry ID required"
}

@test "schedule update without entry id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule update
  assert_failure
  assert_output_contains "Entry ID required"
}


# Create validation

@test "schedule create without summary shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule create --starts-at "2024-01-15T10:00:00Z" --ends-at "2024-01-15T11:00:00Z"
  assert_failure
  assert_output_contains "Summary required"
}

@test "schedule create without starts-at shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule create "Test Event" --ends-at "2024-01-15T11:00:00Z"
  assert_failure
  assert_output_contains "--starts-at required"
}

@test "schedule create without ends-at shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule create "Test Event" --starts-at "2024-01-15T10:00:00Z"
  assert_failure
  assert_output_contains "--ends-at required"
}


# Settings validation

@test "schedule settings without include-due shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule settings
  assert_failure
  assert_output_contains "--include-due required"
}


# Update validation

@test "schedule update without any fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq schedule update 456
  assert_failure
  assert_output_contains "No update fields provided"
}


# Help flag

@test "schedule --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule --help
  assert_success
  assert_output_contains "bcq schedule"
  assert_output_contains "entries"
  assert_output_contains "create"
  assert_output_contains "update"
}


# Unknown action

@test "schedule unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq schedule foobar
  assert_failure
  assert_output_contains "Unknown schedule action"
}
