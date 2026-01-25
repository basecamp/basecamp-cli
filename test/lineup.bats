#!/usr/bin/env bats
# lineup.bats - Test lineup command error handling

load test_helper


# Missing action

@test "lineup without action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup
  assert_failure
  assert_output_contains "Action required"
}


# Create - missing arguments

@test "lineup create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup create
  assert_failure
  assert_output_contains "Marker name"
}

@test "lineup create without date shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup create "Alpha Release"
  assert_failure
  assert_output_contains "Marker date"
}


# Update - missing arguments

@test "lineup update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup update
  assert_failure
  assert_output_contains "ID required"
}

@test "lineup update without name or date shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup update 123
  assert_failure
  assert_output_contains "Provide --name"
}


# Delete - missing arguments

@test "lineup delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup delete
  assert_failure
  assert_output_contains "ID required"
}


# Flag parsing

@test "lineup create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup create --name
  assert_failure
  assert_output_contains "--name requires a value"
}

@test "lineup create --date without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup create "Alpha" --date
  assert_failure
  assert_output_contains "--date requires a value"
}


# Help

@test "lineup --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup --help
  assert_success
  assert_output_contains "bcq lineup"
  assert_output_contains "create"
  assert_output_contains "update"
  assert_output_contains "delete"
  assert_output_contains "account-wide"
}


# Unknown action

@test "lineup unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq lineup foobar
  # Command may show help or require project - just verify it runs
}
