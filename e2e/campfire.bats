#!/usr/bin/env bats
# campfire.bats - Test campfire command error handling

load test_helper


# Flag parsing errors

@test "campfire --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire list --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "campfire messages --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp campfire messages --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Missing context errors

@test "campfire list without project and without --all shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire list
  assert_failure
  assert_output_contains "project"
}

@test "campfire messages without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire messages
  assert_failure
  assert_output_contains "project"
}

@test "campfire post without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp campfire post
  assert_failure
  assert_json_value '.error' '<message> required'
  assert_json_value '.code' 'usage'
}


# Line show/delete errors

@test "campfire line without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp campfire line
  assert_failure
  assert_output_contains "ID required"
}

@test "campfire delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp campfire delete
  assert_failure
  assert_output_contains "ID required"
}


# Help flag

@test "campfire --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire --help
  assert_success
  assert_output_contains "basecamp campfire"
  assert_output_contains "Campfire"
}

@test "campfire -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire -h
  assert_success
  assert_output_contains "basecamp campfire"
}

@test "campfire post help documents --content-type flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire post --help
  assert_success
  assert_output_contains "--content-type"
  assert_output_contains "rich text"
}

@test "campfire list help documents --all flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire list --help
  assert_success
  assert_output_contains "--all"
  assert_output_contains "account"
}


# Unknown action - Cobra treats unknown args as command arguments, not subcommands

@test "campfire unknown action shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire foobar
  # Parent command with no RunE — cobra shows help for unknown subcommands
  assert_success
}


# Error envelope structure

@test "campfire error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire list
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
