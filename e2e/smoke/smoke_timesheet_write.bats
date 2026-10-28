#!/usr/bin/env bats
# smoke_timesheet_write.bats - Level 1: Timesheet entry lifecycle on a to-do

load smoke_helper

setup_file() {
  # Each test depends on the one before it: create writes the entry id the
  # rest read, and delete must come last.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true
  ensure_token || return 1
  ensure_todo || return 1
}

@test "timesheet entry create logs time against a todo" {
  run_smoke basecamp timesheet entry create "$QA_TODO" --hours 0.25 --description "Smoke entry $(date +%s)" --json
  # Timesheets may not be enabled on all accounts (403 forbidden)
  [[ "$status" -eq 4 ]] && mark_unverifiable "Timesheets not enabled in this account"
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/timesheet_entry_id"
}

@test "timesheet entry show returns entry detail" {
  local id_file="$BATS_FILE_TMPDIR/timesheet_entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No timesheet entry created in prior test"
  local entry_id
  entry_id=$(<"$id_file")

  run_smoke basecamp timesheet entry show "$entry_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.parent.id' "$QA_TODO"
}

@test "timesheet entry update changes the hours" {
  local id_file="$BATS_FILE_TMPDIR/timesheet_entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No timesheet entry created in prior test"
  local entry_id
  entry_id=$(<"$id_file")

  run_smoke basecamp timesheet entry update "$entry_id" --hours 0:30 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "timesheet entry delete removes the entry" {
  local id_file="$BATS_FILE_TMPDIR/timesheet_entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No timesheet entry created in prior test"
  local entry_id
  entry_id=$(<"$id_file")

  run_smoke basecamp timesheet entry delete "$entry_id" --force --json
  assert_success
  assert_json_value '.data.deleted' 'true'
}
