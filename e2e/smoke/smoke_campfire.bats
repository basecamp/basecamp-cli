#!/usr/bin/env bats
# smoke_campfire.bats - Level 0/1: Campfire (chat) operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_campfire || return 1
}

@test "campfire list returns campfires" {
  run_smoke basecamp campfire list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire messages returns lines" {
  run_smoke basecamp campfire messages --chat "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire post creates a message" {
  run_smoke basecamp campfire post "Smoke test $(date +%s)" \
    --chat "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/campfire_line_id"
}

@test "campfire line shows a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id
  line_id=$(<"$id_file")

  run_smoke basecamp campfire line "$line_id" \
    --chat "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "campfire delete deletes a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id
  line_id=$(<"$id_file")

  run_smoke basecamp campfire delete "$line_id" \
    --chat "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire upload uploads to campfire" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_campfire_upload.txt"
  echo "campfire upload test $(date +%s)" > "$tmpfile"

  run_smoke basecamp campfire upload "$tmpfile" \
    --chat "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
