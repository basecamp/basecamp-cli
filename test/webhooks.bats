#!/usr/bin/env bats
# webhooks.bats - Test webhook command error handling

load test_helper


# Flag parsing errors

@test "webhooks --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq webhooks --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "webhooks create --url without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks create --url
  assert_failure
  assert_output_contains "--url requires a value"
}

@test "webhooks create --types without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks create --url https://example.com/hook --types
  assert_failure
  assert_output_contains "--types requires a value"
}


# Missing context errors

@test "webhooks without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq webhooks
  assert_failure
  assert_output_contains "No project specified"
}

@test "webhooks show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks show
  assert_failure
  assert_output_contains "Webhook ID required"
}

@test "webhooks create without url shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks create
  assert_failure
  assert_output_contains "Webhook URL required"
}

@test "webhooks update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks update --url https://example.com/hook
  assert_failure
  assert_output_contains "Webhook ID required"
}

@test "webhooks delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks delete
  assert_failure
  assert_output_contains "Webhook ID required"
}


# Help flag

@test "webhooks --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq webhooks --help
  assert_success
  assert_output_contains "bcq webhooks"
  assert_output_contains "Webhook payload URL"
}

@test "webhooks -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq webhooks -h
  assert_success
  assert_output_contains "bcq webhooks"
}


# Unknown action

@test "webhooks unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq webhooks foobar
  assert_failure
  assert_output_contains "Unknown webhooks action"
}


# Error envelope structure

@test "webhooks error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq webhooks
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
