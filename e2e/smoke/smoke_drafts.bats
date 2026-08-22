#!/usr/bin/env bats
# smoke_drafts.bats - Level 0: Personal draft listing

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "drafts list returns drafts" {
  run_smoke basecamp drafts list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "drafts list honors --limit" {
  run_smoke basecamp drafts list --limit 1 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "drafts list rejects --page 0" {
  # Page 0 is the SDK's "fetch every page" spelling. Only --all may reach it,
  # so asking for it by number is a usage error rather than a full crawl.
  run_smoke basecamp drafts list --page 0 --json
  assert_failure
}

@test "drafts list rejects --page with --all" {
  run_smoke basecamp drafts list --page 1 --all --json
  assert_failure
}
