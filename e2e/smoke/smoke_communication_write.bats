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

@test "boost delete removes a boost" {
  local id_file="$BATS_FILE_TMPDIR/boost_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No boost created in prior test"
  local boost_id
  boost_id=$(<"$id_file")

  run_smoke basecamp boost delete "$boost_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- React shortcut ---

@test "react creates a boost via shortcut" {
  run_smoke basecamp react "🎉" --on "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
