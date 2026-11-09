#!/usr/bin/env bats
# smoke_comments.bats - Level 1: Comment operations across types

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "comments create on todo creates comment" {
  # First create a todo to comment on
  local todo_out
  todo_out=$(basecamp todos create "Comment target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot create todo for comment test"
    return
  }
  local todo_id
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || mark_unverifiable "No todo ID returned"

  run_smoke basecamp comments create "$todo_id" "Smoke comment $(date +%s)" -p "$QA_PROJECT" --json
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

@test "comments thread returns reply-ready context" {
  local id_file="$BATS_FILE_TMPDIR/comment_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No comment created in prior test"
  local comment_id
  comment_id=$(<"$id_file")

  run_smoke basecamp comments thread "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'

  # A reply routes to the parent recording, never the comment.
  assert_json_value '.data.recording_full' 'true'
  assert_json_not_null '.data.focus'

  local reply_target recording_id
  reply_target=$(echo "$output" | jq -r '.data.reply_target.recording_id')
  recording_id=$(echo "$output" | jq -r '.data.recording.id')
  [[ "$reply_target" == "$recording_id" ]] \
    || fail "reply_target.recording_id ($reply_target) != recording.id ($recording_id)"
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

@test "comments create creates a second comment for archive tests" {
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

@test "comments guarded replacement refuses stale versions before writing" {
  local todo_out todo_id comment_out comment_id baseline unchanged
  todo_out=$(basecamp todos create "Guard target $(date +%s)" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json)
  todo_id=$(echo "$todo_out" | jq -r '.data.id // empty')
  [[ -n "$todo_id" ]] || fail "No synthetic target returned"
  comment_out=$(basecamp comments create "$todo_id" "Original synthetic comment" -p "$QA_PROJECT" --json)
  comment_id=$(echo "$comment_out" | jq -r '.data.id // empty')
  baseline=$(echo "$comment_out" | jq -r '.data.updated_at // empty')
  [[ -n "$comment_id" && -n "$baseline" ]] || fail "Missing comment/version"

  run_smoke basecamp comments update "$comment_id" "Matching version writes" \
    --if-unmodified-since "$baseline" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  unchanged=$(basecamp comments show "$comment_id" -p "$QA_PROJECT" --json | jq -c '.data | {content,updated_at}')

  run_smoke basecamp comments update "$comment_id" "Must not replace human text" \
    --if-unmodified-since '2000-01-01T00:00:00Z' -p "$QA_PROJECT" --json
  assert_failure
  [[ "$output" == *conflict* ]] || fail "Expected version conflict"
  [[ "$(basecamp comments show "$comment_id" -p "$QA_PROJECT" --json | jq -c '.data | {content,updated_at}')" == "$unchanged" ]] || fail "Refusal changed the comment"

  run_smoke basecamp comments update "$comment_id" "Explicit override writes" \
    --if-unmodified-since '2000-01-01T00:00:00Z' --force -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  run_smoke basecamp comments show "$comment_id" -p "$QA_PROJECT" --json
  assert_success
  [[ "$output" == *"Explicit override writes"* ]] || fail "Override not stored"
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
