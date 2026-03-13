#!/usr/bin/env bats
# smoke_cards_write.bats - Level 1: Card CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || return 1
}

@test "card create creates a card (shortcut)" {
  [[ -n "${QA_CARDTABLE:-}" ]] || mark_unverifiable "No card table in project $QA_PROJECT"

  run_smoke basecamp card "Smoke card $(date +%s)" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/card_id"
}

@test "cards create creates a card (direct verb)" {
  [[ -n "${QA_CARDTABLE:-}" ]] || mark_unverifiable "No card table in project $QA_PROJECT"

  run_smoke basecamp cards create "Smoke direct card $(date +%s)" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/direct_card_id"
}

@test "cards show returns card detail" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards show "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "cards columns lists columns" {
  run_smoke basecamp cards columns --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'

  # Capture first column for move test
  echo "$output" | jq -r '.data[0].id // empty' > "$BATS_FILE_TMPDIR/column_id"
}

@test "cards move moves a card to a column" {
  local card_file="$BATS_FILE_TMPDIR/direct_card_id"
  local col_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$card_file" ]] || mark_unverifiable "No card created in prior test"
  [[ -f "$col_file" ]] || mark_unverifiable "No column discovered in prior test"
  local card_id col_id
  card_id=$(<"$card_file")
  col_id=$(<"$col_file")
  [[ -n "$col_id" ]] || mark_unverifiable "Column ID is empty"

  run_smoke basecamp cards move "$card_id" --to "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step create creates a step on a card" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards step create "Smoke step $(date +%s)" \
    --card "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/step_id"
}

@test "cards steps lists steps on a card" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards steps "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step complete completes a step" {
  local id_file="$BATS_FILE_TMPDIR/step_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No step created in prior test"
  local step_id
  step_id=$(<"$id_file")

  run_smoke basecamp cards step complete "$step_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step uncomplete uncompletes a step" {
  local id_file="$BATS_FILE_TMPDIR/step_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No step created in prior test"
  local step_id
  step_id=$(<"$id_file")

  run_smoke basecamp cards step uncomplete "$step_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards trash trashes a card" {
  local id_file="$BATS_FILE_TMPDIR/direct_card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards trash "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards restore restores a trashed card" {
  local id_file="$BATS_FILE_TMPDIR/direct_card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards restore "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "card update updates a card (shortcut)" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp card update "$card_id" \
    --title "Updated shortcut $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "card move moves a card (shortcut)" {
  local card_file="$BATS_FILE_TMPDIR/card_id"
  local col_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$card_file" ]] || mark_unverifiable "No card created in prior test"
  [[ -f "$col_file" ]] || mark_unverifiable "No column discovered in prior test"
  local card_id col_id
  card_id=$(<"$card_file")
  col_id=$(<"$col_file")
  [[ -n "$col_id" ]] || mark_unverifiable "Column ID is empty"

  run_smoke basecamp card move "$card_id" --to "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards update updates a card (direct verb)" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards update "$card_id" \
    --title "Updated direct $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step update updates a step" {
  local id_file="$BATS_FILE_TMPDIR/step_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No step created in prior test"
  local step_id
  step_id=$(<"$id_file")

  run_smoke basecamp cards step update "$step_id" \
    "Updated step $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step move moves a step" {
  local id_file="$BATS_FILE_TMPDIR/step_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No step created in prior test"
  local step_id
  step_id=$(<"$id_file")

  run_smoke basecamp cards step move "$step_id" --position 1 -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards step delete deletes a step" {
  local id_file="$BATS_FILE_TMPDIR/step_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No step created in prior test"
  local step_id
  step_id=$(<"$id_file")

  run_smoke basecamp cards step delete "$step_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards archive archives a card" {
  local id_file="$BATS_FILE_TMPDIR/card_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No card created in prior test"
  local card_id
  card_id=$(<"$id_file")

  run_smoke basecamp cards archive "$card_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
