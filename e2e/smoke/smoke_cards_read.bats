#!/usr/bin/env bats
# smoke_cards_read.bats - Level 0: Cards, columns, steps (read-only)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || mark_unverifiable "No card table available"
}

@test "cards list returns cards" {
  run_smoke basecamp cards list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
