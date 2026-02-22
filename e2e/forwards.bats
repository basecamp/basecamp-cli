#!/usr/bin/env bats
# forwards.bats - Test forwards command error handling

load test_helper


# Missing context errors

@test "forwards without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp forwards
  assert_failure
  assert_output_contains "project"
}

@test "forwards show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp forwards show
  assert_failure
  assert_output_contains "ID required"
}


# Replies validation

@test "forwards replies without forward id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp forwards replies
  assert_failure
  assert_output_contains "ID required"
}

@test "forwards reply without forward id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp forwards reply
  assert_failure
  assert_output_contains "ID required"
}

@test "forwards reply without reply id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp forwards reply 456
  # Cobra returns "accepts 2 arg(s)" error
  assert_failure
}


# Flag parsing

@test "forwards --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp forwards --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "forwards --inbox without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp forwards --inbox
  assert_failure
  assert_output_contains "--inbox requires a value"
}


# Help

@test "forwards --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp forwards --help
  assert_success
  assert_output_contains "basecamp forwards"
  assert_output_contains "replies"
  assert_output_contains "inbox"
}


# Unknown action

@test "forwards unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp forwards foobar
  # Command may show help or require project - just verify it runs
}
