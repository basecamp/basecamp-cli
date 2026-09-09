#!/usr/bin/env bats
# people.bats - people command error handling (offline: every case resolves
# locally, before any request)

load test_helper


# Help

@test "people clients without subcommand shows help" {
  run basecamp people clients
  assert_success
  assert_output_contains "COMMANDS"
}


# Missing context errors

@test "people clients list without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp people clients list --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' '--project (or --in) is required'
}

@test "people clients add without ids shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp people clients add --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' '<id|email|name>... required'
}


# Invitee parsing

@test "people clients invite rejects a token that is not an address" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp people clients invite "Annie Bryan" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error | contains("Name <email>")' 'true'
}

@test "people clients invite - with empty pipe is a usage error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf '' | basecamp people clients invite - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "empty"
}

@test "people clients invite - mixed with other invitees is rejected" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf 'a@example.com' | basecamp people clients invite - b@example.com --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "cannot be combined"
}

@test "people clients invite --title with several invitees is rejected" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp people clients invite a@example.com b@example.com --title Owner --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "--title"
}
