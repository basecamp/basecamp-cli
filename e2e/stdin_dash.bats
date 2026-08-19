#!/usr/bin/env bats
# stdin_dash.bats - "-" (read from stdin) support and the tier-2 dash guard.
#
# Tier 1: content inputs accept "-" to read piped stdin. Tier 2: everywhere
# else, a literal "-" combined with piped stdin is a usage error instead of
# silently becoming literal content. All cases here fail before any HTTP, so
# no cassette or server is needed.

load test_helper


# Tier 1 — "-" resolves against stdin

@test "todos create - with empty pipe is a usage error, not an empty todo" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf '' | basecamp todos create - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "empty"
}

@test "comments create - on a TTY-like stdin errors immediately instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # /dev/null is a character device — the TTY stand-in. Must not block.
  run bash -c "basecamp comments create 123 - --json < /dev/null"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "nothing is piped"
}

@test "comments create with a bare pipe and no dash teaches the dash" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf 'hello' | basecamp comments create 123 --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.hint | contains("pass \"-\"")' 'true'
}


# Tier 2 — stray literal "-" with a pipe is rejected

@test "projects create - with piped stdin is rejected with the -- escape" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "printf 'x' | basecamp projects create - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error | contains("<name>")' 'true'
  assert_json_value '.hint | contains("after the -- separator")' 'true'
}

@test "projects create -- - with piped stdin passes the guard" {
  # The -- separator makes the "-" literal; the command proceeds past the
  # guard and fails on the (unreachable) API instead of on usage.
  export BASECAMP_BASE_URL="http://127.0.0.1:1"
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "printf 'x' | basecamp projects create --json -- -"
  assert_failure
  [[ "$output" != *'does not read stdin'* ]]
}
