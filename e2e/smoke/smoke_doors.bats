#!/usr/bin/env bats
# smoke_doors.bats - Level 1: External links (doors) in a project's dock

load smoke_helper

setup_file() {
  # Each test depends on the one before it: create writes the door id the
  # rest read, and delete must come last.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true
  ensure_token || return 1
  ensure_project || return 1
}

@test "doors create adds an external link" {
  run_smoke basecamp doors create --url "https://example.com/smoke-$(date +%s)" \
    --title "Smoke link" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.service.code' 'other'
  assert_json_value '.data.visible_to_clients' 'false'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/door_id"
}

@test "doors list lists external links" {
  run_smoke basecamp doors list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "doors show returns an external link" {
  local id_file="$BATS_FILE_TMPDIR/door_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No external link created in prior test"
  local door_id
  door_id=$(<"$id_file")

  run_smoke basecamp doors show "$door_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.data.id' "$door_id"
}

@test "doors update renames an external link" {
  local id_file="$BATS_FILE_TMPDIR/door_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No external link created in prior test"
  local door_id
  door_id=$(<"$id_file")

  run_smoke basecamp doors update "$door_id" --title "Smoke link renamed" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.data.title' 'Smoke link renamed'
}

@test "doors delete deletes an external link" {
  local id_file="$BATS_FILE_TMPDIR/door_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No external link created in prior test"
  local door_id
  door_id=$(<"$id_file")

  run_smoke basecamp doors delete "$door_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
