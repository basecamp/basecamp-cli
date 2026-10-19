#!/usr/bin/env bats
# pings.bats - Test pings command input handling

load test_helper


# Help

@test "pings without subcommand shows help" {
  run basecamp pings
  assert_success
  assert_output_contains "COMMANDS"
  assert_output_contains "start"
}

@test "ping is an alias of pings" {
  run basecamp ping
  assert_success
  assert_output_contains "COMMANDS"
}

@test "pings list help says when a ping appears" {
  run basecamp pings list --help
  assert_success
  assert_output_contains "once someone has posted"
}

@test "chat list help points to pings" {
  run basecamp chat list --help
  assert_success
  assert_output_contains "basecamp pings list"
}


# Input errors that need no network

@test "pings start --new-group needs two people" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp pings start Jane --new-group
  assert_failure
  assert_output_contains "--new-group needs two or more people"
}

@test "pings show rejects a URL that is not a ping" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp pings show https://3.basecamp.com/99999/buckets/1/todos/2
  assert_failure
  assert_output_contains "expected a ping URL"
}

@test "pings show rejects a URL on an untrusted host" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp pings show https://evil.example.com/99999/circles/1
  assert_failure
  assert_output_contains "untrusted host"
}

@test "pings messages --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp pings messages 1 --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}
