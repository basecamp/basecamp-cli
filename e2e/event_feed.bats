#!/usr/bin/env bats
# event_feed.bats - Account event feed: entry-point and filter validation
#
# Every case here is refused before a request leaves the process, so the suite
# needs no network and no live feed.

load test_helper

setup_extra() {
  create_credentials
  create_global_config '{"account_id": 99999}'
}

@test "events poll rejects --since and --position together" {
  run basecamp events poll --since now --position abc
  assert_failure
  assert_output_contains "mutually exclusive"
}

@test "events poll rejects a --since that is not an entry point" {
  run basecamp events poll --since 2026-07-14
  assert_failure
  assert_output_contains "Invalid --since"
}

@test "events poll rejects a non-numeric bucket filter" {
  run basecamp events poll --buckets one
  assert_failure
  assert_output_contains "Invalid --buckets id"
}

@test "events poll rejects an actor type outside agent and person" {
  run basecamp events poll --actor-types robot
  assert_failure
  assert_output_contains "Invalid --actor-types"
}

@test "events poll --agent --help documents position handling" {
  run basecamp events poll --agent --help
  assert_success
  assert_output_contains "position"
  assert_output_contains "Deduplicate by event id"
}

@test "events ticket --agent --help warns against logging the ticket" {
  run basecamp events ticket --agent --help
  assert_success
  assert_output_contains "show-secret"
}

@test "inbox rejects --since and --position together" {
  run basecamp inbox --since now --position abc
  assert_failure
  assert_output_contains "mutually exclusive"
}

@test "inbox --agent --help documents addressing_id dedupe" {
  run basecamp inbox --agent --help
  assert_success
  assert_output_contains "addressing_id"
  assert_output_contains "agent"
}
