#!/usr/bin/env bats
# smoke_attachments.bats - Level 1: Attachment listing on items

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "attachments without subcommand shows help" {
  run basecamp attachments
  assert_success
  assert_output_contains "list"
}

@test "attachments list on comment with attachment shows results" {
  # Create a todo to comment on
  local todo_out
  todo_out=$(basecamp todo "Attach test $(date +%s)" --list "$QA_TODOLIST" \
    -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo for attachment test"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  # Create a small test file and attach it via comment
  local tmpfile="$BATS_FILE_TMPDIR/test-attachment.txt"
  echo "smoke test content $(date +%s)" > "$tmpfile"
  basecamp comment "$todo_id" "See attached file" --attach "$tmpfile" \
    -p "$QA_PROJECT" --json 2>/dev/null || {
    mark_unverifiable "Cannot create comment with attachment"
    return
  }

  # Get the comment ID (most recent comment on the todo)
  local comments_out
  comments_out=$(basecamp comments list "$todo_id" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list comments"
    return
  }
  local comment_id
  comment_id=$(echo "$comments_out" | jq -r '.data[-1].id // empty')
  [[ -n "$comment_id" ]] || mark_unverifiable "No comment ID found"

  # List attachments on the comment
  run_smoke basecamp attachments list "$comment_id" --type comment --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data | length > 0' 'true'
}

@test "attachments list on item with no attachments returns empty" {
  # Create a plain todo with no attachments
  local todo_out
  todo_out=$(basecamp todo "No attach $(date +%s)" --list "$QA_TODOLIST" \
    -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  run_smoke basecamp attachments list "$todo_id" --type todo --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data' '[]'
}
