#!/usr/bin/env bats
# tools.bats - Test tools command help output

load test_helper

# Help

@test "tools without subcommand shows help" {
  run basecamp tools
  assert_success
  assert_output_contains "COMMANDS"
}

# Help flag

@test "tools --help shows subcommand hint in usage" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp tools --help
  assert_success
  assert_output_contains "basecamp tools <command>"
  assert_output_contains "dock tools"
}

@test "tools -h shows subcommand hint in usage" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp tools -h
  assert_success
  assert_output_contains "basecamp tools <command>"
}

# Create validation

@test "tools create --visible-to-clients without --type shows usage error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp tools create --visible-to-clients
  assert_failure
  assert_output_contains "--type is required"
}
