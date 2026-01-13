#!/usr/bin/env bats
# forwards.bats - Test forwards command error handling

load test_helper


# Missing context errors

@test "forwards without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq forwards
  assert_failure
  assert_output_contains "No project specified"
}

@test "forwards show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq forwards show
  assert_failure
  assert_output_contains "Forward ID required"
}


# Replies validation

@test "forwards replies without forward id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq forwards replies
  assert_failure
  assert_output_contains "Forward ID required"
}

@test "forwards reply without forward id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq forwards reply
  assert_failure
  assert_output_contains "Forward ID required"
}

@test "forwards reply without reply id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq forwards reply 456
  assert_failure
  assert_output_contains "Reply ID required"
}


# Flag parsing

@test "forwards --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq forwards --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "forwards --inbox without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq forwards --inbox
  assert_failure
  assert_output_contains "--inbox requires a value"
}


# Help

@test "forwards --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq forwards --help
  assert_success
  assert_output_contains "bcq forwards"
  assert_output_contains "replies"
  assert_output_contains "inbox"
}


# Unknown action

@test "forwards unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq forwards foobar
  assert_failure
  assert_output_contains "Unknown forwards action"
}
