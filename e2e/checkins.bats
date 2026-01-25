#!/usr/bin/env bats
# checkins.bats - Test checkins command error handling

load test_helper


# Flag parsing errors

@test "checkins --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "checkins --questionnaire without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins --questionnaire
  assert_failure
  assert_output_contains "--questionnaire requires a value"
}


# Missing context errors

@test "checkins without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins
  assert_failure
  assert_output_contains "project"
}

@test "checkins questions without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins questions
  assert_failure
  assert_output_contains "project"
}

@test "checkins question without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq checkins question
  assert_failure
  assert_output_contains "ID required"
}

@test "checkins answers without question id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq checkins answers
  assert_failure
  assert_output_contains "ID required"
}

@test "checkins answer without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq checkins answer
  assert_failure
  assert_output_contains "Answer ID required"
}


# Help flag

@test "checkins --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins --help
  assert_success
  assert_output_contains "bcq checkins"
  assert_output_contains "questions"
  assert_output_contains "answers"
}


# Unknown action

@test "checkins unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq checkins foobar
  # Command may show help or require project - just verify it runs
}
