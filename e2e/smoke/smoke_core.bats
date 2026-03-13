#!/usr/bin/env bats
# smoke_core.bats - Level 0: Core commands (doctor, auth status, config, version)

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "version shows version info" {
  run_smoke basecamp --version
  assert_success
  assert_output_contains "basecamp"
}

@test "version command shows version info" {
  run_smoke basecamp version
  assert_success
  assert_output_contains "basecamp"
}

@test "status shows JSON envelope" {
  run_smoke basecamp --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.version'
}

@test "auth status shows authenticated" {
  run_smoke basecamp auth status --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "config show returns config" {
  run_smoke basecamp config show --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "config project shows project config" {
  ensure_project || return 0
  run_smoke basecamp config project -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "doctor runs checks" {
  run_smoke basecamp doctor --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "config init creates local config" {
  run_smoke basecamp config init --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "config set and unset round-trips a value" {
  run_smoke basecamp config set hints false --json
  assert_success
  assert_json_value '.ok' 'true'

  run_smoke basecamp config unset hints --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "commands list returns command catalog" {
  run_smoke basecamp commands --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "help shows usage" {
  run_smoke basecamp help
  assert_success
  assert_output_contains "USAGE"
}
