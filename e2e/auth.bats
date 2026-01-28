#!/usr/bin/env bats
# auth.bats - Tests for auth commands

load test_helper


# Auth token

@test "bcq auth token --help shows help" {
  run bcq auth token --help
  assert_success
  assert_output_contains "Print the current access token"
  assert_output_contains "--stored"
  assert_output_contains "--host"  # global flag shown in help
}

@test "bcq auth token fails when not authenticated" {
  run bcq auth token
  assert_failure
  assert_exit_code 3  # ExitAuth
  assert_output_contains "Not authenticated"
}

@test "bcq auth token returns BASECAMP_TOKEN env var" {
  export BASECAMP_TOKEN="test-token-12345"
  run bcq auth token
  assert_success
  # Output should be exactly the token (single line, raw)
  [[ "$output" == "test-token-12345" ]]
}

@test "bcq auth token outputs single line only" {
  export BASECAMP_TOKEN="test-token-67890"
  run bcq auth token
  assert_success
  # Count lines - should be exactly 1
  local line_count
  line_count=$(echo "$output" | wc -l | tr -d ' ')
  [[ "$line_count" -eq 1 ]]
}

@test "bcq auth token --stored fails when no stored credentials" {
  export BASECAMP_TOKEN="env-token"
  run bcq auth token --stored
  assert_failure
  assert_output_contains "No stored credentials"
}

@test "bcq --host uses BASECAMP_TOKEN if set" {
  export BASECAMP_TOKEN="env-token-for-host"
  run bcq --host localhost:3000 auth token
  assert_success
  [[ "$output" == "env-token-for-host" ]]
}

@test "bcq --host fails when not authenticated" {
  run bcq --host localhost:3000 auth token
  assert_failure
  assert_output_contains "Not authenticated"
}

@test "bcq --host --stored bypasses env and checks stored" {
  export BASECAMP_TOKEN="env-token"
  run bcq --host staging.example.com auth token --stored
  assert_failure
  assert_output_contains "No stored credentials for https://staging.example.com"
}


# Auth status

@test "bcq auth status shows not authenticated" {
  run bcq auth status
  assert_success
  assert_output_contains "authenticated"
}

@test "bcq auth status shows BASECAMP_TOKEN source" {
  export BASECAMP_TOKEN="test-token"
  run bcq auth status
  assert_success
  assert_output_contains "BASECAMP_TOKEN"
}


# Host normalization

@test "bcq --base-url works as alias for --host" {
  export BASECAMP_TOKEN="alias-test-token"
  run bcq --base-url http://localhost:3000 auth token
  assert_success
  [[ "$output" == "alias-test-token" ]]
}

@test "bcq --host normalizes IPv6 loopback to http" {
  run bcq --host '[::1]:3000' auth status
  assert_success
  assert_output_contains '"origin": "http://[::1]:3000"'
}

@test "bcq --host normalizes localhost to http" {
  run bcq --host localhost:3000 auth status
  assert_success
  assert_output_contains '"origin": "http://localhost:3000"'
}

@test "bcq --host normalizes bare hostname to https" {
  run bcq --host api.example.com auth status
  assert_success
  assert_output_contains '"origin": "https://api.example.com"'
}
