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

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/comment_id"
  echo "$todo_id" > "$BATS_FILE_TMPDIR/comment_todo_id"
}

@test "comments show returns comment detail" {
  local id_file="$BATS_FILE_TMPDIR/comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments show "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "comments update updates a comment" {
  local id_file="$BATS_FILE_TMPDIR/comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments update "$comment_id" \
    "Updated smoke comment $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "comments create creates a comment (direct verb)" {
  local todo_file="$BATS_FILE_TMPDIR/comment_todo_id"
  [[ -f "$todo_file" ]] || mark_unverifiable "No todo created for comment test"
  local todo_id
  todo_id=$(<"$todo_file")

  run_smoke basecamp comments create "$todo_id" \
    "Direct comment $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/direct_comment_id"
}

@test "comments archive archives a comment" {
  local id_file="$BATS_FILE_TMPDIR/direct_comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments archive "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "comments restore restores an archived comment" {
  local id_file="$BATS_FILE_TMPDIR/direct_comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments restore "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "comments trash trashes a comment" {
  local id_file="$BATS_FILE_TMPDIR/direct_comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments trash "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
