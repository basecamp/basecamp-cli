#!/usr/bin/env bats
# smoke_filings.bats - Level 0: Reading a move's or copy's filing

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "filings show reports a filing it cannot read as not found" {
  run_smoke basecamp filings show 999999999999 --json
  assert_failure
  assert_json_value '.code' 'not_found'
  assert_output_contains "Only the person who started a move or copy"
}
