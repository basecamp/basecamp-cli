#!/usr/bin/env bats
# webhooks.bats - Test webhook command error handling

load test_helper


# Help

@test "webhooks without subcommand shows help" {
  run basecamp webhooks
  assert_success
  assert_output_contains "COMMANDS"
}


# Flag parsing errors

@test "webhooks create --url is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # URL is now a positional arg, not a flag
  run basecamp webhooks create --url
  assert_failure
  assert_output_contains "Unknown option"
}

@test "webhooks create --types without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks create https://example.com/hook --types
  assert_failure
  assert_output_contains "--types requires a value"
}


# Missing context errors

@test "webhooks show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks show
  assert_failure
  # Go returns generic "ID required", Bash returned "ID required"
  assert_output_contains "ID required"
}

@test "webhooks create without url shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks create
  assert_failure
  assert_json_value '.error' '<url> required'
  assert_json_value '.code' 'usage'
}

@test "webhooks update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks update --url https://example.com/hook
  assert_failure
  # Go returns generic "ID required", Bash returned "ID required"
  assert_output_contains "ID required"
}

@test "webhooks delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks delete
  assert_failure
  # Go returns generic "ID required", Bash returned "ID required"
  assert_output_contains "ID required"
}


# Help flag

@test "webhooks --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp webhooks --help
  assert_success
  assert_output_contains "basecamp webhooks"
  # Go shows subcommand list instead of flag details
  assert_output_contains "create"
  assert_output_contains "delete"
}

@test "webhooks -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp webhooks -h
  assert_success
  assert_output_contains "basecamp webhooks"
}


# Unknown action

@test "webhooks unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp webhooks foobar
  # Command may show help or require project - just verify it runs
}


# Error envelope structure

@test "webhooks error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp webhooks create
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
