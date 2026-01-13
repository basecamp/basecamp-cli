#!/usr/bin/env bats
# campfire.bats - Test campfire command error handling

load test_helper


# Flag parsing errors

@test "campfire --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire list --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "campfire messages --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire messages --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Missing context errors

@test "campfire list without project and without --all shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire list
  assert_failure
  assert_output_contains "No project specified"
  assert_output_contains "--all"
}

@test "campfire messages without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire messages
  assert_failure
  assert_output_contains "No project specified"
}

@test "campfire post without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire post
  assert_failure
  assert_output_contains "Message content required"
}


# Line show/delete errors

@test "campfire line without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire line
  assert_failure
  assert_output_contains "Line ID required"
}

@test "campfire delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire delete
  assert_failure
  assert_output_contains "Line ID required"
}


# Help flag

@test "campfire --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire --help
  assert_success
  assert_output_contains "bcq campfire"
  assert_output_contains "Campfire"
}

@test "campfire -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire -h
  assert_success
  assert_output_contains "bcq campfire"
}

@test "campfire help documents --all flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire --help
  assert_success
  assert_output_contains "--all"
  assert_output_contains "account"
}


# Unknown action

@test "campfire unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire foobar
  assert_failure
  assert_output_contains "Unknown campfire action"
}


# Error envelope structure

@test "campfire error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire list
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
