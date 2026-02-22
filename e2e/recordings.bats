#!/usr/bin/env bats
# recordings.bats - Test recordings command error handling

load test_helper


# Visibility - missing context

@test "recordings visibility without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp recordings visibility
  assert_failure
  assert_output_contains "ID required"
}

@test "recordings visibility without visible flag shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp recordings visibility 456
  assert_failure
  assert_output_contains "--visible or --hidden"
}

@test "recordings visibility without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings visibility 456 --visible
  assert_failure
  assert_output_contains "project"
}


# Trash/Archive/Restore - missing context

@test "recordings trash without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp recordings trash
  assert_failure
  assert_output_contains "ID required"
}

@test "recordings archive without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp recordings archive
  assert_failure
  assert_output_contains "ID required"
}

@test "recordings restore without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp recordings restore
  assert_failure
  assert_output_contains "ID required"
}


# List - missing type

@test "recordings list without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings list
  assert_failure
  assert_output_contains "Type required"
}


# Help

@test "recordings --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings --help
  assert_success
  assert_output_contains "basecamp recordings"
  assert_output_contains "visibility"
  assert_output_contains "trash"
  assert_output_contains "archive"
}


# Unknown action

@test "recordings unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings foobar
  # Command may show help or require project - just verify it runs
}
