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
  todo_out=$(basecamp todos create "Assign target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
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

@test "assign handles multiple todos in one command" {
  # Create two fresh todos
  local todo1_out todo2_out
  todo1_out=$(basecamp todos create "Batch assign 1 $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo 1 for batch assign test"
    return
  }
  todo2_out=$(basecamp todos create "Batch assign 2 $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo 2 for batch assign test"
    return
  }
  local id1 id2
  id1=$(echo "$todo1_out" | jq -r '.data.id // empty')
  id2=$(echo "$todo2_out" | jq -r '.data.id // empty')
  [[ -n "$id1" && -n "$id2" ]] || mark_unverifiable "No todo IDs returned"

  echo "$id1" > "$BATS_FILE_TMPDIR/batch_assign_id1"
  echo "$id2" > "$BATS_FILE_TMPDIR/batch_assign_id2"

  run_smoke basecamp assign "$id1" "$id2" --to me -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "unassign handles multiple todos in one command" {
  local id1_file="$BATS_FILE_TMPDIR/batch_assign_id1"
  local id2_file="$BATS_FILE_TMPDIR/batch_assign_id2"
  [[ -f "$id1_file" && -f "$id2_file" ]] || mark_unverifiable "No todos created in prior test"
  local id1 id2
  id1=$(<"$id1_file")
  id2=$(<"$id2_file")

  run_smoke basecamp unassign "$id1" "$id2" --from me -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
