#!/usr/bin/env bats
# core.bats - Tests for lib/core.sh

load test_helper


# Version

@test "bcq shows version" {
  run bcq version
  assert_success
  assert_output_contains "bcq"
}


# Quick start

@test "bcq with no args shows quick start" {
  run bcq
  assert_success
  assert_output_contains "bcq"
}

@test "bcq --json with no args outputs JSON" {
  run bcq --json
  assert_success
  is_valid_json
  assert_json_not_null ".version"
}


# Help

@test "bcq --help shows help" {
  run bcq --help
  assert_success
  assert_output_contains "USAGE"
  assert_output_contains "COMMANDS"
}

@test "bcq help shows main help" {
  run bcq help
  assert_success
  assert_output_contains "bcq"
}


# Output format detection

@test "bcq defaults to markdown when TTY" {
  # This is tricky to test since bats runs in non-TTY
  # For now, just verify --md flag works
  run bcq --md
  assert_success
  assert_output_not_contains '"version"'
}

@test "bcq --json forces JSON output" {
  run bcq --json
  assert_success
  is_valid_json
}


# Global flags

@test "bcq respects --quiet flag" {
  run bcq --quiet version
  assert_success
}

@test "bcq respects --verbose flag" {
  run bcq --verbose version
  assert_success
}


# Error handling

@test "bcq unknown command shows error" {
  run bcq notacommand
  assert_failure
}


# JSON envelope structure

@test "JSON output has correct envelope structure" {
  run bcq --json
  assert_success
  is_valid_json

  # Check required fields
  assert_json_not_null ".version"
  assert_json_not_null ".auth"
  assert_json_not_null ".context"
}
