#!/usr/bin/env bats
# auth.bats - Tests for auth commands

load test_helper


# Help

@test "auth without subcommand shows help" {
  run basecamp auth
  assert_success
  assert_output_contains "COMMANDS"
}


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


# Auth login

@test "basecamp auth login --help shows --remote, --local, and --device-code" {
  run basecamp auth login --help
  assert_success
  assert_output_contains "--remote"
  assert_output_contains "--local"
  assert_output_contains "--device-code"
}

@test "basecamp login --help shows --remote, --local, and --device-code" {
  run basecamp login --help
  assert_success
  assert_output_contains "--remote"
  assert_output_contains "--local"
  assert_output_contains "--device-code"
}

@test "basecamp auth login --help describes flags provider-neutrally" {
  run basecamp auth login --help
  assert_success
  assert_output_contains "Headless authentication with manual browser instructions"
  assert_output_contains "ignored by Launchpad"
  assert_output_contains "default full"
}

@test "basecamp profile create --help describes flags provider-neutrally" {
  run basecamp profile create --help
  assert_success
  assert_output_contains "Headless authentication with manual browser instructions"
  assert_output_contains "ignored by Launchpad"
  assert_output_contains "default full"
}

@test "basecamp auth login --help shows --with-token, --expect-identity, and --login-hint" {
  run basecamp auth login --help
  assert_success
  assert_output_contains "--with-token"
  assert_output_contains "--expect-identity"
  assert_output_contains "--login-hint"
  assert_output_contains "op read"
}

@test "basecamp auth login --with-token requires a profile" {
  run basecamp auth login --with-token </dev/null
  assert_failure
  assert_output_contains "named profile"
  assert_output_contains "--profile"
}

@test "basecamp auth login --with-token needs --account to create the profile" {
  run basecamp auth login --with-token -P bot </dev/null
  assert_failure
  assert_output_contains "does not exist"
  assert_output_contains "--account"
}

@test "basecamp auth login --with-token rejects --device-code" {
  run basecamp auth login --with-token --device-code </dev/null
  assert_failure
  assert_output_contains "with-token"
}

@test "basecamp auth login rejects --device-code --local" {
  run basecamp auth login --device-code --local
  assert_failure
  assert_output_contains "device-code"
  assert_output_contains "local"
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
