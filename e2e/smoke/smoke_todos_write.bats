#!/usr/bin/env bats
# smoke_todos_write.bats - Level 1: Todo CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "todo create creates a todo" {
  run_smoke basecamp todo "Smoke test todo $(date +%s)" \
    --list "$QA_TODOLIST" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  # Persist ID for subsequent tests (BATS runs each @test in a subshell)
  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/todo_id"
}

@test "todos create creates a todo (direct verb)" {
  run_smoke basecamp todos create "Smoke direct todo $(date +%s)" \
    --list "$QA_TODOLIST" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/direct_todo_id"
}

@test "todo complete marks todo done" {
  local todo_id_file="$BATS_FILE_TMPDIR/todo_id"
  [[ -f "$todo_id_file" ]] || mark_unverifiable "No todo created in prior test"
  local todo_id
  todo_id=$(<"$todo_id_file")

  run_smoke basecamp done "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todo reopen marks todo active" {
  local todo_id_file="$BATS_FILE_TMPDIR/todo_id"
  [[ -f "$todo_id_file" ]] || mark_unverifiable "No todo created in prior test"
  local todo_id
  todo_id=$(<"$todo_id_file")

  run_smoke basecamp reopen "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos complete marks todo done (direct verb)" {
  local id_file="$BATS_FILE_TMPDIR/direct_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No direct todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp todos complete "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos uncomplete marks todo active (direct verb)" {
  local id_file="$BATS_FILE_TMPDIR/direct_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No direct todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp todos uncomplete "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos position moves a todo" {
  local id_file="$BATS_FILE_TMPDIR/direct_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No direct todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp todos position "$todo_id" --to 1 -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos trash trashes a todo" {
  local id_file="$BATS_FILE_TMPDIR/direct_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No direct todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp todos trash "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todos restore restores a trashed todo" {
  local id_file="$BATS_FILE_TMPDIR/direct_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No direct todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp todos restore "$todo_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todolists create creates a todolist" {
  run_smoke basecamp todolists create "Smoke todolist $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
