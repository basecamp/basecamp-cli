#!/usr/bin/env bats
# chat.bats - Test chat command error handling

load test_helper


# Help

@test "chat without subcommand shows help" {
  run basecamp chat
  assert_success
  assert_output_contains "COMMANDS"
}


# Flag parsing errors

@test "chat --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat list --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "chat messages --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat messages --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Missing context errors

@test "chat list without project and without --all shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat list
  assert_failure
  assert_output_contains "project"
}

@test "chat messages without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat messages
  assert_failure
  assert_output_contains "project"
}

@test "chat post without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat post
  assert_failure
  assert_json_value '.error' '<message> required'
  assert_json_value '.code' 'usage'
}

@test "chat post with whitespace-only content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat post " "
  assert_failure
  assert_json_value '.error' '<message> required'
  assert_json_value '.code' 'usage'
}


# Line show/delete errors

@test "chat line without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat line
  assert_failure
  assert_output_contains "ID required"
}

@test "chat delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat delete
  assert_failure
  assert_output_contains "ID required"
}

@test "chat update without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp chat update
  assert_failure
  assert_output_contains "required"
}


# Help flag

@test "chat --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat --help
  assert_success
  assert_output_contains "basecamp chat"
  assert_output_contains "chat"
}

@test "chat -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat -h
  assert_success
  assert_output_contains "basecamp chat"
}

@test "chat post help documents --content-type flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat post --help
  assert_success
  assert_output_contains "--content-type"
  assert_output_contains "rich text"
}

@test "chat list help documents --all flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat list --help
  assert_success
  assert_output_contains "--all"
  assert_output_contains "account"
}


# Unknown action - Cobra treats unknown args as command arguments, not subcommands

@test "chat unknown action shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat foobar
  # Parent command with no RunE — cobra shows help for unknown subcommands
  assert_success
}


# Error envelope structure

@test "chat error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp chat list
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}

@test "campfire alias --help shows canonical chat help" {
  run basecamp campfire --help
  assert_success
  assert_output_contains "basecamp chat"
}
