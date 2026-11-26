#!/usr/bin/env bats
# smoke_campfire.bats - Level 0/1: Campfire (chat) operations

load smoke_helper

setup_file() {
  # The line and chatbot lifecycles each depend on the test before: create
  # writes the id the rest read, and delete must come last.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true
  ensure_token || return 1
  ensure_project || return 1
  ensure_campfire || return 1
}

@test "campfire list returns campfires" {
  run_smoke basecamp campfire list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire messages returns lines" {
  run_smoke basecamp campfire messages --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire post creates a message" {
  run_smoke basecamp campfire post "Smoke test $(date +%s)" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/campfire_line_id"
}

@test "campfire line shows a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id
  line_id=$(<"$id_file")

  run_smoke basecamp campfire line "$line_id" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "campfire update edits a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id new_content
  line_id=$(<"$id_file")
  new_content="Edited smoke test $(date +%s)"

  run_smoke basecamp campfire update "$line_id" "$new_content" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  # Re-fetch the line and verify its content actually changed (guards against
  # a no-op update silently passing).
  run_smoke basecamp campfire line "$line_id" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  echo "$output" | jq -e --arg expected "$new_content" '.data.content | contains($expected)' >/dev/null \
    || fail "expected updated line content to contain '$new_content', got: $(echo "$output" | jq -r '.data.content')"
}

@test "campfire delete deletes a message" {
  local id_file="$BATS_FILE_TMPDIR/campfire_line_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No campfire line created in prior test"
  local line_id
  line_id=$(<"$id_file")

  # --force is required in machine-output mode: the delete is permanent (the API
  # does not trash it) and --json shows no confirmation prompt, so the intent has
  # to be stated. This suite means it — without the flag the delete is refused
  # and the posted test message is left behind.
  run_smoke basecamp campfire delete "$line_id" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json --force
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire upload uploads to campfire" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_campfire_upload.txt"
  echo "campfire upload test $(date +%s)" > "$tmpfile"

  run_smoke basecamp campfire upload "$tmpfile" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire uploads lists files posted in the room" {
  run_smoke basecamp campfire uploads --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# Chatbots belong to the account and managing them takes an administrator, so
# a smoke user who isn't one can't verify the lifecycle. Every step after
# create reads the bot it made, and delete --force leaves none behind.
@test "campfire bots create adds a chatbot" {
  run_smoke basecamp campfire bots create "smoke_$(date +%s)" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  if [[ "$status" -ne 0 ]] && [[ "$(echo "$output" | jq -r '.code // empty' 2>/dev/null)" == "forbidden" ]]; then
    mark_unverifiable "Creating a chatbot needs an account administrator"
  fi
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.lines_url'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/campfire_bot_id"
}

@test "campfire bots list lists chatbots" {
  run_smoke basecamp campfire bots list --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "campfire bots show shows a chatbot" {
  local id_file="$BATS_FILE_TMPDIR/campfire_bot_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No chatbot created in prior test"

  run_smoke basecamp campfire bots show "$(<"$id_file")" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_not_null '.data.service_name'
}

@test "campfire bots update renames a chatbot" {
  local id_file="$BATS_FILE_TMPDIR/campfire_bot_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No chatbot created in prior test"
  local name="smoke_renamed_$(date +%s)"

  run_smoke basecamp campfire bots update "$(<"$id_file")" --name "$name" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.data.service_name' "$name"
}

@test "campfire bots delete deletes a chatbot" {
  local id_file="$BATS_FILE_TMPDIR/campfire_bot_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No chatbot created in prior test"

  run_smoke basecamp campfire bots delete "$(<"$id_file")" \
    --room "$QA_CAMPFIRE" -p "$QA_PROJECT" --json --force
  assert_success
  assert_json_value '.ok' 'true'
}
