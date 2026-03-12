#!/usr/bin/env bats
# smoke_misc_write.bats - Level 1: Schedule settings, recordings trash/restore

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "schedule settings updates schedule settings" {
  ensure_schedule || mark_unverifiable "Cannot discover schedule"
  run_smoke basecamp schedule settings --include-due \
    --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "recordings trash trashes a recording" {
  ensure_todolist || mark_unverifiable "Cannot discover todolist for recordings trash"

  # Create a throwaway todo to trash
  local todo_out
  todo_out=$(basecamp todo "Trash target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo for recordings trash test"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  run_smoke basecamp recordings trash "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'

  echo "$todo_id" > "$BATS_FILE_TMPDIR/trashed_recording_id"
}

@test "recordings restore restores a trashed recording" {
  local id_file="$BATS_FILE_TMPDIR/trashed_recording_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No recording trashed in prior test"
  local rec_id
  rec_id=$(<"$id_file")

  run_smoke basecamp recordings restore "$rec_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
