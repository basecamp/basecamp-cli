#!/usr/bin/env bats
# hillcharts.bats - Test hillcharts command error handling

load test_helper


# Bare command shows help

@test "hillcharts without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts
  assert_success
  assert_output_contains "COMMANDS"
}


# Missing project errors

@test "hillcharts show without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts show
  assert_failure
  assert_output_contains "project"
}

@test "hillcharts show with --todoset skips project requirement" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts show --todoset 12345
  assert_failure
  assert_output_not_contains "project"
}

@test "hillcharts track with --todoset skips project requirement" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts track 111 --todoset 12345
  assert_failure
  assert_output_not_contains "project"
}

@test "hillcharts untrack with --todoset skips project requirement" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts untrack 111 --todoset 12345
  assert_failure
  assert_output_not_contains "project"
}

@test "hillcharts track without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts track
  assert_failure
}

@test "hillcharts untrack without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts untrack
  assert_failure
}


# Progress updates

@test "hillcharts updates without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates
  assert_failure
  assert_output_contains "project"
}

@test "hillcharts updates with --todoset skips project requirement" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates --todoset 12345
  assert_failure
  assert_output_not_contains "project"
}

@test "hillcharts updates rejects --all with --limit" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates --todoset 12345 --all --limit 5
  assert_failure
  assert_output_contains "--all and --limit are mutually exclusive"
}

@test "hillcharts updates with an ID points at show" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates 501 --todoset 12345
  assert_failure
  assert_output_contains "hillcharts updates show"
}

@test "hillcharts updates show without args shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates show
  assert_failure
}

@test "hillcharts updates show refuses an untrusted URL" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates show "https://3.basecamp.example.com/99999/buckets/1/todosets/2/hills/versions#__recording_3"
  assert_failure
  assert_output_contains "untrusted host"
}

@test "hillcharts updates show refuses a URL that is not a progress update" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts updates show "https://3.basecamp.com/99999/buckets/1/todolists/2"
  assert_failure
  assert_output_contains "does not name a hill chart progress update"
}


@test "hillcharts update with nothing to post shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts update --todoset 12345 --json
  assert_failure
  assert_output_contains "No update fields specified"
}

@test "hillcharts update rejects a --dot without a position" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts update --todoset 12345 --dot Design
  assert_failure
  assert_output_contains "needs a position"
}

@test "hillcharts update rejects a position outside 0 to 100" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts update --todoset 12345 --dot "Design=101"
  assert_failure
  assert_output_contains "whole number from 0 to 100"
}

@test "hillcharts update --note - with nothing piped shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp hillcharts update --todoset 12345 --note - < /dev/null
  assert_failure
}


# Help

@test "hillcharts --help shows help" {
  create_credentials
  create_global_config '{}'

  run basecamp hillcharts --help
  assert_success
  assert_output_contains "basecamp hillcharts"
  assert_output_contains "hill chart"
  assert_output_contains "--project"
}
