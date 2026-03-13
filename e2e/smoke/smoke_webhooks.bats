#!/usr/bin/env bats
# smoke_webhooks.bats - Level 1: Webhook CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "webhooks list returns webhooks" {
  run_smoke basecamp webhooks list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "webhooks create creates a webhook" {
  run_smoke basecamp webhooks create "https://smoke-test.invalid/hook-$(date +%s)" \
    --types Todo -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/webhook_id"
}

@test "webhooks show returns webhook detail" {
  local id_file="$BATS_FILE_TMPDIR/webhook_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No webhook created in prior test"
  local webhook_id
  webhook_id=$(<"$id_file")

  run_smoke basecamp webhooks show "$webhook_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "webhooks update updates a webhook" {
  local id_file="$BATS_FILE_TMPDIR/webhook_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No webhook created in prior test"
  local webhook_id
  webhook_id=$(<"$id_file")

  run_smoke basecamp webhooks update "$webhook_id" \
    --url "https://smoke-test.invalid/hook-updated-$(date +%s)" \
    -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "webhooks delete removes a webhook" {
  local id_file="$BATS_FILE_TMPDIR/webhook_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No webhook created in prior test"
  local webhook_id
  webhook_id=$(<"$id_file")

  run_smoke basecamp webhooks delete "$webhook_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
