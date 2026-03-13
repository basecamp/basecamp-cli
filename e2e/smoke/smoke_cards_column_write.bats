#!/usr/bin/env bats
# smoke_cards_column_write.bats - Level 1: Card column CRUD operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || return 1
}

@test "cards column create creates a column" {
  run_smoke basecamp cards column create "Smoke col $(date +%s)" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/column_id"
}

@test "cards column show returns column detail" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column show "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "cards column update updates a column" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column update "$col_id" \
    --title "Updated col $(date +%s)" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column color sets column color" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column color "$col_id" \
    --color "blue" --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column move moves a column" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column move "$col_id" --position 1 \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column watch watches a column" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column watch "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column unwatch unwatches a column" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column unwatch "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column on-hold sets column on hold" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column on-hold "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards column no-on-hold clears column on hold" {
  local id_file="$BATS_FILE_TMPDIR/column_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No column created in prior test"
  local col_id
  col_id=$(<"$id_file")

  run_smoke basecamp cards column no-on-hold "$col_id" \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
