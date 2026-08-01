#!/usr/bin/env bats
# smoke_calendars.bats - Level 0: Calendar read and recolor

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "calendars show rejects a non-id argument" {
  run_smoke basecamp calendars show not-an-id --json
  assert_failure
}

@test "calendars show requires a discoverable calendar" {
  # There is no index endpoint, so nothing here can discover a calendar id.
  mark_unverifiable "No calendars index endpoint to discover an id from"
}

@test "calendars update rejects an unknown color" {
  # Client-side validation: this must fail without issuing a request, since
  # the SDK cannot surface the server's own field message.
  run_smoke basecamp calendars update 999999 --color chartreuse --json
  assert_failure
}
