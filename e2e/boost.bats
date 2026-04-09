#!/usr/bin/env bats
# boost.bats - Test boost command error handling

load test_helper


# Help

@test "boost without subcommand shows help" {
  run basecamp boost
  assert_success
  assert_output_contains "COMMANDS"
}


# Help flag

@test "boost --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp boost --help
  assert_success
  assert_output_contains "basecamp boost"
}
