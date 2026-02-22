#!/usr/bin/env bats
# auth.bats - Tests for auth commands

load test_helper


# Auth token

@test "basecamp auth token --help shows help" {
  run basecamp auth token --help
  assert_success
  assert_output_contains "Print the current access token"
  assert_output_contains "--stored"
  assert_output_contains "--profile"  # global flag shown in help
}

@test "basecamp auth token fails when not authenticated" {
  run basecamp auth token
  assert_failure
  assert_exit_code 3  # ExitAuth
  assert_output_contains "Not authenticated"
}

@test "basecamp auth token returns BASECAMP_TOKEN env var" {
  export BASECAMP_TOKEN="test-token-12345"
  run basecamp auth token
  assert_success
  # Output should be exactly the token (single line, raw)
  [[ "$output" == "test-token-12345" ]]
}

@test "basecamp auth token outputs single line only" {
  export BASECAMP_TOKEN="test-token-67890"
  run basecamp auth token
  assert_success
  # Count lines - should be exactly 1
  local line_count
  line_count=$(echo "$output" | wc -l | tr -d ' ')
  [[ "$line_count" -eq 1 ]]
}

@test "basecamp auth token --stored fails when no stored credentials" {
  export BASECAMP_TOKEN="env-token"
  run basecamp auth token --stored
  assert_failure
  assert_output_contains "No stored credentials"
}


# Auth status

@test "basecamp auth status shows not authenticated" {
  run basecamp auth status
  assert_success
  assert_output_contains "authenticated"
}

@test "basecamp auth status shows BASECAMP_TOKEN source" {
  export BASECAMP_TOKEN="test-token"
  run basecamp auth status
  assert_success
  assert_output_contains "BASECAMP_TOKEN"
}
