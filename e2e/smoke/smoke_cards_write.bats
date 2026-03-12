#!/usr/bin/env bats
# smoke_cards_write.bats - Level 1: Card CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || return 1
}

@test "card create creates a card" {
  [[ -n "${QA_CARDTABLE:-}" ]] || mark_unverifiable "No card table in project $QA_PROJECT"

  run_smoke basecamp card "Smoke card $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
