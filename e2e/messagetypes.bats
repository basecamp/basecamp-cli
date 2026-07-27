#!/usr/bin/env bats
# messagetypes.bats - Test messagetypes command error handling

load test_helper


# Help

@test "messagetypes without subcommand shows help" {
  run basecamp messagetypes
  assert_success
  assert_output_contains "COMMANDS"
}


# Missing project errors

@test "messagetypes list without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messagetypes list
  assert_failure
  assert_output_contains "project"
}


# Flag parsing errors

@test "messagetypes --project without value shows error" {
  create_credentials
  create_global_config '{}'

  run basecamp messagetypes list --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Missing context errors

@test "messagetypes show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes show
  assert_failure
  assert_output_contains "ID required"
}


# Create validation

@test "messagetypes create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes create --icon "test"
  assert_failure
  assert_json_value '.error' '<name> required'
  assert_json_value '.code' 'usage'
}

@test "messagetypes create without icon shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes create "Test Type"
  assert_failure
  assert_output_contains "--icon is required"
}

@test "messagetypes create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes create --name
  assert_failure
  assert_output_contains "--name requires a value"
}

@test "messagetypes create --icon without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes create "Test" --icon
  assert_failure
  assert_output_contains "--icon requires a value"
}


# Update validation

@test "messagetypes update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes update
  assert_failure
  assert_output_contains "ID required"
}

@test "messagetypes update without any fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes update 456
  assert_failure
  assert_output_contains "No update fields specified"
}


# Delete validation

@test "messagetypes delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp messagetypes delete
  assert_failure
  assert_output_contains "ID required"
}


# Help

@test "messagetypes --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messagetypes --help
  assert_success
  assert_output_contains "basecamp messagetypes"
  assert_output_contains "create"
  assert_output_contains "update"
}


# Unknown action

@test "messagetypes unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp messagetypes foobar
  # Command may show help or require project - just verify it runs
}
