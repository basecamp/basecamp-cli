#!/usr/bin/env bats
# errors.bats - Test error handling

load test_helper


# Flag parsing errors

@test "todos --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "todos --list without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --list
  assert_failure
  assert_output_contains "--list requires a value"
}

@test "todos --assignee without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --assignee
  assert_failure
  assert_output_contains "--assignee requires a value"
}

@test "campfire --campfire without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire messages --campfire
  assert_failure
  assert_output_contains "--campfire requires a value"
}

@test "comment --on without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq comment "test" --on
  assert_failure
  assert_output_contains "--on requires a recording ID"
}

@test "cards --column without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq cards --column
  assert_failure
  assert_output_contains "--column requires a value"
}

@test "recordings --type without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings --type
  assert_failure
  assert_output_contains "--type requires a value"
}

@test "recordings --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Global flag errors

@test "bcq --project without value shows error" {
  create_credentials

  run bcq --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "bcq --account without value shows error" {
  create_credentials

  run bcq --account
  assert_failure
  assert_output_contains "--account requires a value"
}


# Missing content errors

@test "todo create without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq todos create
  assert_failure
  assert_output_contains "Todo content required"
}

@test "comment without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq comment --on 123
  assert_failure
  assert_output_contains "Comment content required"
}

@test "campfire post without message shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire post
  assert_failure
  assert_output_contains "Message content required"
}


# Missing context errors

@test "todos without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos
  assert_failure
  assert_output_contains "No project specified"
}

@test "cards without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq cards
  assert_failure
  assert_output_contains "No project specified"
}

@test "recordings without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings
  assert_failure
  assert_output_contains "Type required"
}


# JSON error envelope structure

@test "error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --project
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
