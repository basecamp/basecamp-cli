#!/usr/bin/env bats
# doors.bats - Test doors (external links) command error handling

load test_helper


# Help

@test "doors without subcommand shows help" {
  run basecamp doors
  assert_success
  assert_output_contains "COMMANDS"
}

@test "external-links is an alias for doors" {
  run basecamp external-links --help
  assert_success
  assert_output_contains "external links"
}


# Missing project errors

@test "doors list without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp doors list --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'Project ID required'
}


# Validation before any request

@test "doors create without --url shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp doors create --title "Design" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' '--url is required'
}

@test "doors update without any fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp doors update 456 --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'No update fields specified'
}

@test "doors show with a non-numeric id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp doors show abc --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'Invalid door ID'
}

@test "doors delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp doors delete --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'ID required'
}
