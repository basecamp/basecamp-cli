#!/usr/bin/env bats
# smoke_subtasks.bats - Level 1: Subtask lifecycle on a to-do

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_todo || return 1
}

@test "subtasks create adds a subtask to a to-do" {
  run_smoke basecamp subtasks create "$QA_TODO" "Smoke subtask $(date +%s)" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/subtask_id"
}

@test "subtasks list returns the to-do's subtasks" {
  run_smoke basecamp subtasks list "$QA_TODO" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data | type' 'array'
}

@test "subtasks show returns subtask detail" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks show "$subtask_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.parent.id' "$QA_TODO"
}

@test "subtasks update updates a subtask" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks update "$subtask_id" "Updated subtask $(date +%s)" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "subtasks complete completes a subtask" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks complete "$subtask_id" --json
  assert_success
  assert_json_value '.data.completed' 'true'
}

@test "subtasks uncomplete reopens a subtask" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks uncomplete "$subtask_id" --json
  assert_success
  assert_json_value '.data.completed' 'false'
}

@test "subtasks move moves a subtask to the top" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks move "$subtask_id" --position 1 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "subtasks delete deletes a subtask" {
  local id_file="$BATS_FILE_TMPDIR/subtask_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No subtask created in prior test"
  local subtask_id
  subtask_id=$(<"$id_file")

  run_smoke basecamp subtasks delete "$subtask_id" --force --json
  assert_success
  assert_json_value '.ok' 'true'
}
