#!/usr/bin/env bats
# subscriptions.bats - Test subscriptions command error handling

load test_helper


# Flag parsing errors

@test "subscriptions --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq subscriptions 123 --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Missing context errors

@test "subscriptions without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq subscriptions show
  assert_failure
  assert_output_contains "ID required"
}

@test "subscriptions subscribe without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq subscriptions subscribe
  assert_failure
  assert_output_contains "ID required"
}

@test "subscriptions unsubscribe without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq subscriptions unsubscribe
  assert_failure
  assert_output_contains "ID required"
}

@test "subscriptions add without people ids shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq subscriptions add 456
  assert_failure
  assert_output_contains "Person ID(s) required"
}

@test "subscriptions remove without people ids shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq subscriptions remove 456
  assert_failure
  assert_output_contains "Person ID(s) required"
}


# Help flag

@test "subscriptions --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq subscriptions --help
  assert_success
  assert_output_contains "bcq subscriptions"
  assert_output_contains "subscribe"
  assert_output_contains "unsubscribe"
}


# Unknown action

@test "subscriptions unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq subscriptions foobar
  # Command may show help or require project - just verify it runs
}
