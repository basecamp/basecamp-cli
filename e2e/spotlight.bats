#!/usr/bin/env bats
# spotlight.bats - Test spotlight and unspotlight error handling

load test_helper


@test "spotlight without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp spotlight
  assert_failure
  assert_output_contains "ID required"
}

@test "unspotlight without recording id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp unspotlight
  assert_failure
  assert_output_contains "ID required"
}
