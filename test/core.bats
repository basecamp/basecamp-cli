#!/usr/bin/env bats
# core.bats - Tests for lib/core.sh

load test_helper


# Version

@test "bcq shows version" {
  run bcq --version
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
  assert_json_not_null ".data.version"
}


# Help

@test "bcq --help shows help" {
  run bcq --help
  assert_success
  assert_output_contains "Available Commands"
  assert_output_contains "bcq"
}

@test "bcq help shows main help" {
  run bcq help
  assert_success
  assert_output_contains "bcq"
}


# Output format detection

@test "bcq --json forces JSON output" {
  run bcq --json
  assert_success
  is_valid_json
}


# Global flags

@test "bcq respects --quiet flag" {
  run bcq --quiet --help
  assert_success
}

@test "bcq respects --verbose flag" {
  run bcq --verbose --help
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

  # Check required fields (nested under .data in Go binary)
  assert_json_not_null ".data.version"
  assert_json_not_null ".data.auth"
  assert_json_not_null ".data.context"
}


# Verbose mode

@test "verbose mode shows HTTP requests" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Run with verbose and capture stderr
  run bash -c "bcq -v projects 2>&1"

  # Go binary uses [bcq] prefix for verbose output
  assert_output_contains "[bcq]"
  assert_output_contains "GET"
}
