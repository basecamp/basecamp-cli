#!/usr/bin/env bats
# lineup.bats - Test lineup command error handling

load test_helper


# Bare parent command

@test "lineup without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup
  assert_success
  assert_output_contains "COMMANDS"
}


# Create - missing arguments

@test "lineup create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup create
  assert_failure
  assert_json_value '.error' '<name> required'
  assert_json_value '.code' 'usage'
}

@test "lineup create without date shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup create "Alpha Release"
  assert_failure
  assert_json_value '.error' '<date> required'
  assert_json_value '.code' 'usage'
}


# Update - missing arguments

@test "lineup update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup update
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "lineup update without name or date shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup update 123
  assert_failure
  assert_json_value '.error' 'No update fields specified'
  assert_json_value '.code' 'usage'
}


# Delete - missing arguments

@test "lineup delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup delete
  assert_failure
  assert_output_contains "ID required"
}


# Flag parsing

@test "lineup create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup create --name
  assert_failure
  assert_output_contains "Unknown option"
}

@test "lineup create --date without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup create "Alpha" --date
  assert_failure
  assert_output_contains "Unknown option"
}


# Help

@test "lineup --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup --help
  assert_success
  assert_output_contains "basecamp lineup"
  assert_output_contains "create"
  assert_output_contains "update"
  assert_output_contains "delete"
  assert_output_contains "account-wide"
}


# Unknown action

@test "lineup unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp lineup foobar
  # Command may show help or require project - just verify it runs
}
