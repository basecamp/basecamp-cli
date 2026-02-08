#!/usr/bin/env bats
# boost.bats - Test boost command error handling

load test_helper


# react shortcut errors

@test "react without --on or --recording shows usage error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq react "👍"
  assert_failure
  assert_output_contains "--on or --recording required"
}


# Help flag

@test "boost --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq boost --help
  assert_success
  assert_output_contains "bcq boost"
}

@test "react --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq react --help
  assert_success
  assert_output_contains "--on"
  assert_output_contains "--recording"
}
