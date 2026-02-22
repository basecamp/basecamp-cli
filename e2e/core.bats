#!/usr/bin/env bats
# core.bats - Tests for lib/core.sh

load test_helper


# Version

@test "basecamp shows version" {
  run basecamp --version
  assert_success
  assert_output_contains "basecamp"
}


# Quick start

@test "basecamp with no args shows quick start" {
  run basecamp
  assert_success
  assert_output_contains "basecamp"
}

@test "basecamp --json with no args outputs JSON" {
  run basecamp --json
  assert_success
  is_valid_json
  assert_json_not_null ".data.version"
}


# Help

@test "basecamp --help shows help" {
  run basecamp --help
  assert_success
  assert_output_contains "Available Commands"
  assert_output_contains "basecamp"
}

@test "basecamp help shows main help" {
  run basecamp help
  assert_success
  assert_output_contains "basecamp"
}


# Output format detection

@test "basecamp --json forces JSON output" {
  run basecamp --json
  assert_success
  is_valid_json
}


# Global flags

@test "basecamp respects --quiet flag" {
  run basecamp --quiet --help
  assert_success
}

@test "basecamp respects --verbose flag" {
  run basecamp --verbose --help
  assert_success
}


# Error handling

@test "basecamp unknown command shows error" {
  run basecamp notacommand
  assert_failure
}


# JSON envelope structure

@test "JSON output has correct envelope structure" {
  run basecamp --json
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
  run bash -c "basecamp -v projects 2>&1"

  # Check for SDK operation tracing output (format: [timestamp] Calling/Failed <Service>.<Method>)
  assert_output_contains "Calling"
  assert_output_contains "Projects.List"
}
