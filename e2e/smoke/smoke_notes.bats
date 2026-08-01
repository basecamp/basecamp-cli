#!/usr/bin/env bats
# smoke_notes.bats - Level 0: Personal note

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "notes show returns the note" {
  # An account that has never written a note returns an empty one rather than
  # a 404, so this must succeed either way.
  run_smoke basecamp notes show --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "notes set is out of scope" {
  mark_out_of_scope "Mutating - set replaces the whole note; covered by the live round-trip"
}
