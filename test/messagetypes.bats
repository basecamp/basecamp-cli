#!/usr/bin/env bats
# messagetypes.bats - Test messagetypes command error handling

load test_helper


# Missing context errors

@test "messagetypes without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq messagetypes
  assert_failure
  assert_output_contains "No project specified"
}

@test "messagetypes show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes show
  assert_failure
  assert_output_contains "Message type ID required"
}


# Create validation

@test "messagetypes create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes create --icon "test"
  assert_failure
  assert_output_contains "Name required"
}

@test "messagetypes create without icon shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes create "Test Type"
  assert_failure
  assert_output_contains "--icon required"
}

@test "messagetypes create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes create --name
  assert_failure
  assert_output_contains "--name requires a value"
}

@test "messagetypes create --icon without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes create "Test" --icon
  assert_failure
  assert_output_contains "--icon requires a value"
}


# Update validation

@test "messagetypes update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes update
  assert_failure
  assert_output_contains "Message type ID required"
}

@test "messagetypes update without any fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes update 456
  assert_failure
  assert_output_contains "No update fields provided"
}


# Delete validation

@test "messagetypes delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq messagetypes delete
  assert_failure
  assert_output_contains "Message type ID required"
}


# Flag parsing

@test "messagetypes --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq messagetypes --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Help

@test "messagetypes --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq messagetypes --help
  assert_success
  assert_output_contains "bcq messagetypes"
  assert_output_contains "create"
  assert_output_contains "update"
  assert_output_contains "--icon"
}


# Unknown action

@test "messagetypes unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq messagetypes foobar
  assert_failure
  assert_output_contains "Unknown messagetypes action"
}
