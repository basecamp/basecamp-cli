#!/usr/bin/env bats
# smoke_cards_read.bats - Level 0: Cards, columns, steps (read-only)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || mark_unverifiable "No card table available"
}

@test "cards list returns cards" {
  run_smoke basecamp cards list --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards columns returns columns" {
  run_smoke basecamp cards columns --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards show returns card detail" {
  # Discover a card from the list
  local out
  out=$(basecamp cards list --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || mark_unverifiable "Cannot list cards"
  local card_id
  card_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$card_id" ]] || mark_unverifiable "No cards in project"

  run_smoke basecamp cards show "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
