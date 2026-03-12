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
    --content "Automated smoke test" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
