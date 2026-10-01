#!/usr/bin/env bats
# subtasks.bats - Test subtasks command error handling

load test_helper


# Help

@test "subtasks without subcommand shows help" {
  run basecamp subtasks
  assert_success
  assert_output_contains "COMMANDS"
  assert_output_contains "uncomplete"
}


# List errors

@test "subtasks list without parent shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks list
  assert_failure
  assert_json_value '.error' '<todo-or-card-id|url> required'
  assert_json_value '.code' 'usage'
}

@test "subtasks list rejects --all with --limit" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks list 123 --all --limit 5
  assert_failure
  assert_output_contains "mutually exclusive"
}


# Show errors

@test "subtasks show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks show
  assert_failure
  assert_output_contains "ID required"
}

@test "subtasks show rejects a non-id" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks show not-an-id
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "not a subtask id"
}


# Create errors

@test "subtasks create without parent shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks create
  assert_failure
  assert_json_value '.error' '<todo-or-card-id|url> required'
  assert_json_value '.code' 'usage'
}

@test "subtasks create without title shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks create 123
  assert_failure
  assert_json_value '.error' '<title> required'
  assert_json_value '.code' 'usage'
}


# Update errors

@test "subtasks update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks update
  assert_failure
  assert_json_value '.error' '<id|url> required'
}

@test "subtasks update without changes shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks update 456
  assert_failure
  assert_output_contains "No update fields specified"
}

@test "subtasks update rejects --due with --no-due" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks update 456 --due tomorrow --no-due
  assert_failure
  assert_output_contains "mutually exclusive"
}


# Complete/uncomplete errors

@test "subtasks complete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks complete
  assert_failure
  assert_output_contains "ID required"
}

@test "subtasks uncomplete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks uncomplete
  assert_failure
  assert_output_contains "ID required"
}


# Move errors

@test "subtasks move without position shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks move 456
  assert_failure
  assert_output_contains "--position is required (1-based)"
}

@test "subtasks move rejects position 0" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks move 456 --position 0
  assert_failure
  assert_output_contains "--position must be 1 or more"
}


# Delete errors

@test "subtasks delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks delete
  assert_failure
  assert_output_contains "ID required"
}

@test "subtasks delete in JSON mode needs --force" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks delete 456 --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "--force"
}

@test "subtasks delete refuses a parent URL" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp subtasks delete https://3.basecamp.com/99999/buckets/89/todos/123 --force
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "does not name a subtask"
}
