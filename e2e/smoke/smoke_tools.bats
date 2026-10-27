#!/usr/bin/env bats
# smoke_tools.bats - Level 1: Project tools (dock items)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "tools show returns a tool" {
  ensure_messageboard || return 0
  run_smoke basecamp tools show "$QA_MESSAGEBOARD" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "tools create creates a tool" {
  run_smoke basecamp tools create "Smoke tool $(date +%s)" \
    --type message_board -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/tool_id"
}

@test "tools update updates a tool" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools update "$tool_id" \
    "Updated tool $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "tools reposition repositions a tool" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools reposition "$tool_id" --position 1 -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "tools move repositions a tool in its dock" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools move "$tool_id" --position 1 -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "tools move refuses moving a tool to the project it is in" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  # A cross-project move would take the tool out of the QA project; the move
  # itself is covered against local bc3.
  run_smoke basecamp tools move "$tool_id" --to-project "$QA_PROJECT" --json
  assert_failure
  assert_json_value '.ok' 'false'
}

@test "tools disable disables a tool" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools disable "$tool_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "tools enable enables a tool" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools enable "$tool_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "tools trash trashes a tool" {
  local id_file="$BATS_FILE_TMPDIR/tool_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No tool created in prior test"
  local tool_id
  tool_id=$(<"$id_file")

  run_smoke basecamp tools trash "$tool_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
