#!/usr/bin/env bats
# smoke_messages_write.bats - Level 1: Message CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_messageboard || return 1
}

@test "message create creates a message" {
  run_smoke basecamp messages create "Smoke test $(date +%s)" \
    "Automated smoke test" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/message_id"
}

@test "message shortcut creates a message" {
  run_smoke basecamp message "Smoke shortcut $(date +%s)" \
    -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "messages update updates a message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages update "$msg_id" \
    --title "Smoke updated $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages pin pins a message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages pin "$msg_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages unpin unpins a message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages unpin "$msg_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages archive archives a message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages archive "$msg_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages restore restores an archived message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages restore "$msg_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "messages trash trashes a message" {
  local id_file="$BATS_FILE_TMPDIR/message_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No message created in prior test"
  local msg_id
  msg_id=$(<"$id_file")

  run_smoke basecamp messages trash "$msg_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
