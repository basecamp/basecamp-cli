#!/usr/bin/env bats
# smoke_lineup.bats - Level 1: Lineup CRUD lifecycle

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "lineup create creates a lineup marker" {
  run_smoke basecamp lineup create "Smoke lineup $(date +%s)" "tomorrow" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/lineup_id"
}

@test "lineup update updates a lineup marker" {
  local id_file="$BATS_FILE_TMPDIR/lineup_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No lineup created in prior test"
  local lineup_id
  lineup_id=$(<"$id_file")

  run_smoke basecamp lineup update "$lineup_id" "Updated lineup $(date +%s)" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "lineup delete removes a lineup marker" {
  local id_file="$BATS_FILE_TMPDIR/lineup_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No lineup created in prior test"
  local lineup_id
  lineup_id=$(<"$id_file")

  run_smoke basecamp lineup delete "$lineup_id" --json
  assert_success
  assert_json_value '.ok' 'true'
}
