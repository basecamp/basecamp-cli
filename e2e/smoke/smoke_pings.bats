#!/usr/bin/env bats
# smoke_pings.bats - Level 0: Pings (private conversations)

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "pings list returns pings" {
  run_smoke basecamp pings list --json
  assert_success
  assert_json_value '.ok' 'true'

  echo "$output" | jq -r '.data[0].id // empty' > "$BATS_FILE_TMPDIR/ping_id"
}

@test "pings show returns a ping" {
  local id_file="$BATS_FILE_TMPDIR/ping_id"
  [[ -s "$id_file" ]] || mark_unverifiable "No pings to show"
  local ping_id
  ping_id=$(<"$id_file")

  run_smoke basecamp pings show "$ping_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.campfire_id'
}

@test "pings messages returns lines" {
  local id_file="$BATS_FILE_TMPDIR/ping_id"
  [[ -s "$id_file" ]] || mark_unverifiable "No pings to read"
  local ping_id
  ping_id=$(<"$id_file")

  run_smoke basecamp pings messages "$ping_id" --limit 1 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "pings start is out of scope" {
  mark_out_of_scope "Mutating - opens or creates a ping with a real person; covered by the live round-trip"
}

@test "pings post is out of scope" {
  mark_out_of_scope "Mutating - posts into a private conversation with a real person; covered by the live round-trip"
}

@test "pings upload is out of scope" {
  mark_out_of_scope "Mutating - uploads into a private conversation with a real person; covered by the live round-trip"
}
