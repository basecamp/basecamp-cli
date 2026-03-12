#!/usr/bin/env bats
# smoke_account.bats - Level 2: Account-scoped operations (people, templates)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "people list returns people" {
  run_smoke basecamp people list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data[0].id'
}

@test "templates list returns templates" {
  run_smoke basecamp templates list --json
  assert_success
  assert_json_value '.ok' 'true'
}
