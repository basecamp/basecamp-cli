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
  run_smoke basecamp campfire messages --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire post creates a message" {
  run_smoke basecamp campfire post "Smoke test $(date +%s)" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
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
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "campfire update edits a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id new_content
  line_id=$(<"$id_file")
  new_content="Edited smoke test $(date +%s)"

  run_smoke basecamp campfire update "$line_id" "$new_content" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  # Re-fetch the line and verify its content actually changed (guards against
  # a no-op update silently passing).
  run_smoke basecamp campfire line "$line_id" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  echo "$output" | jq -e --arg expected "$new_content" '.data.content | contains($expected)' >/dev/null \
    || fail "expected updated line content to contain '$new_content', got: $(echo "$output" | jq -r '.data.content')"
}

@test "campfire delete deletes a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id
  line_id=$(<"$id_file")

  run_smoke basecamp campfire delete "$line_id" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire upload uploads to campfire" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_campfire_upload.txt"
  echo "campfire upload test $(date +%s)" > "$tmpfile"

  run_smoke basecamp campfire upload "$tmpfile" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
