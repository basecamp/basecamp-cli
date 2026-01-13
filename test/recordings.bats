#!/usr/bin/env bats
# recordings.bats - Test recordings command error handling

load test_helper


# Visibility - missing context

@test "recordings visibility without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq recordings visibility
  assert_failure
  assert_output_contains "Recording ID required"
}

@test "recordings visibility without visible flag shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq recordings visibility 456
  assert_failure
  assert_output_contains "Visibility required"
}

@test "recordings visibility without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings visibility 456 --visible
  assert_failure
  assert_output_contains "No project specified"
}


# Trash/Archive/Restore - missing context

@test "recordings trash without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq recordings trash
  assert_failure
  assert_output_contains "Recording ID required"
}

@test "recordings archive without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq recordings archive
  assert_failure
  assert_output_contains "Recording ID required"
}

@test "recordings restore without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq recordings restore
  assert_failure
  assert_output_contains "Recording ID required"
}


# List - missing type

@test "recordings list without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings list
  assert_failure
  assert_output_contains "Type required"
}


# Help

@test "recordings --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings --help
  assert_success
  assert_output_contains "bcq recordings"
  assert_output_contains "visibility"
  assert_output_contains "trash"
  assert_output_contains "archive"
}


# Unknown action

@test "recordings unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings foobar
  assert_failure
  assert_output_contains "Unknown recordings action"
}
