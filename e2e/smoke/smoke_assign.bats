#!/usr/bin/env bats
# smoke_assign.bats - Level 1: Assign and unassign operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "assign assigns a person to a todo" {
  # Create a fresh todo for assignment
  local todo_out
  todo_out=$(basecamp todo "Assign target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo for assign test"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  echo "$todo_id" > "$BATS_FILE_TMPDIR/assign_todo_id"

  run_smoke basecamp assign "$todo_id" --to me -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "unassign removes a person from a todo" {
  local id_file="$BATS_FILE_TMPDIR/assign_todo_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No todo created in prior test"
  local todo_id
  todo_id=$(<"$id_file")

  run_smoke basecamp unassign "$todo_id" --from me -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
