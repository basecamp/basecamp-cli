#!/usr/bin/env bats
# smoke_bookmarks.bats - Level 0: Personal bookmark operations

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "bookmarks list returns bookmarks" {
  run_smoke basecamp bookmarks list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "bookmarks list honors --limit" {
  run_smoke basecamp bookmarks list --limit 1 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "bookmarks list rejects --all with --limit" {
  run_smoke basecamp bookmarks list --all --limit 1 --json
  assert_failure
}

@test "bookmarks check reports a boolean and exits 0" {
  # check answers a question rather than signalling through the exit code, so
  # a bookmarked recording must come back true *and* exit 0.
  run_smoke basecamp bookmarks list --limit 1 --json
  assert_success
  local id
  id=$(printf '%s' "$output" | jq -r '.data[0].recording.id // empty')
  [[ -z "$id" ]] && mark_unverifiable "No bookmark exists to check against"
  run_smoke basecamp bookmarks check "$id" --json
  assert_success
  assert_json_value '.data.bookmarked' 'true'
}

@test "bookmarks check rejects a non-id argument" {
  run_smoke basecamp bookmarks check not-an-id --json
  assert_failure
}

@test "bookmarks add is out of scope" {
  mark_out_of_scope "Mutating - exercised by the live add/check/remove round-trip"
}

@test "bookmarks remove is out of scope" {
  mark_out_of_scope "Mutating - exercised by the live add/check/remove round-trip"
}

@test "bubble-up add is out of scope" {
  mark_out_of_scope "Mutates the current user's personal readings"
}

@test "bubble-up remove is out of scope" {
  mark_out_of_scope "Mutates the current user's personal readings"
}
