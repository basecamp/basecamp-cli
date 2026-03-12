#!/usr/bin/env bats
# smoke_comments.bats - Level 1: Comment operations across types

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "comment on todo creates comment" {
  # First create a todo to comment on
  local todo_out
  todo_out=$(basecamp todo "Comment target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo for comment test"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  run_smoke basecamp comment "$todo_id" "Smoke comment $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
