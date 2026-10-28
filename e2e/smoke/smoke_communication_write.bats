#!/usr/bin/env bats
# smoke_communication_write.bats - Level 1: Subscriptions, boosts, react mutations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todo || return 1
}

# --- Subscriptions ---

@test "subscriptions subscribe subscribes to a recording" {
  run_smoke basecamp subscriptions subscribe "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "subscriptions unsubscribe unsubscribes from a recording" {
  run_smoke basecamp subscriptions unsubscribe "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Boosts ---

@test "boost create creates a boost on a recording" {
  run_smoke basecamp boost create "$QA_TODO" "👍" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/boost_id"
}

@test "boost show returns boost detail" {
  local id_file="$BATS_FILE_TMPDIR/boost_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No boost created in prior test"
  local boost_id
  boost_id=$(<"$id_file")

  run_smoke basecamp boost show "$boost_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "boost update changes a boost's content" {
  local id_file="$BATS_FILE_TMPDIR/boost_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No boost created in prior test"
  local boost_id
  boost_id=$(<"$id_file")

  run_smoke basecamp boost update "$boost_id" "✅ done" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.content' '✅ done'
}

@test "boost delete removes a boost" {
  local id_file="$BATS_FILE_TMPDIR/boost_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No boost created in prior test"
  local boost_id
  boost_id=$(<"$id_file")

  run_smoke basecamp boost delete "$boost_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Subscriptions (add/remove) ---

@test "subscriptions add adds a subscriber" {
  ensure_person || return 0
  run_smoke basecamp subscriptions add "$QA_TODO" --person "$QA_PERSON" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "subscriptions remove removes a subscriber" {
  ensure_person || return 0
  run_smoke basecamp subscriptions remove "$QA_TODO" --person "$QA_PERSON" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Email forwards ---

@test "forwards copy copies a forward into its own inbox" {
  ensure_inbox || return 0
  run basecamp forwards list --inbox "$QA_INBOX" -p "$QA_PROJECT" --jq '.data[0].id // empty'
  local fwd_id="$output"
  [[ -n "$fwd_id" ]] || mark_unverifiable "No forwards in project inbox"

  run_smoke basecamp forwards copy "$fwd_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.source_id' "$fwd_id"

  run_smoke basecamp recordings trash "$(echo "$output" | jq -r '.data.id')" -p "$QA_PROJECT" --json
  assert_success
}

@test "forwards move refuses the inbox the forward is in" {
  ensure_inbox || return 0
  run basecamp forwards list --inbox "$QA_INBOX" -p "$QA_PROJECT" --jq '.data[0].id // empty'
  local fwd_id="$output"
  [[ -n "$fwd_id" ]] || mark_unverifiable "No forwards in project inbox"

  # A cross-project move would take the QA forward away; the move itself is
  # covered against local bc3.
  run_smoke basecamp forwards move "$fwd_id" --to-inbox "$QA_INBOX" --json
  assert_failure
  assert_json_value '.ok' 'false'
}
