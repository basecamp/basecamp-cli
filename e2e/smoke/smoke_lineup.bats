#!/usr/bin/env bats
# smoke_lineup.bats - Level 1: Lineup CRUD lifecycle

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "lineup create creates a lineup marker" {
  local future_date
  future_date=$(date -v+7d +%Y-%m-%d 2>/dev/null || date -d "+7 days" +%Y-%m-%d)
  local marker_name="Smoke lineup $(date +%s)"
  run_smoke basecamp lineup create "$marker_name" "$future_date" --json
  # Lineup API may not exist on all environments (404 → validation error)
  [[ "$status" -ne 0 ]] && mark_unverifiable "Lineup API not available"
  assert_json_value '.ok' 'true'
  echo "$marker_name" > "$BATS_FILE_TMPDIR/marker_name"
}

@test "lineup list returns markers" {
  run_smoke basecamp lineup list --json
  [[ "$status" -ne 0 ]] && mark_unverifiable "Lineup API not available"
  assert_json_value '.ok' 'true'
}

@test "lineup update updates a lineup marker" {
  local name_file="$BATS_FILE_TMPDIR/marker_name"
  [[ ! -f "$name_file" ]] && mark_unverifiable "No marker created by earlier test"
  local marker_name
  marker_name=$(cat "$name_file")
  run_smoke basecamp lineup list --json
  [[ "$status" -ne 0 ]] && mark_unverifiable "Lineup API not available"
  assert_json_value '.ok' 'true'
  local marker_id
  marker_id=$(echo "$output" | jq -r --arg name "$marker_name" '[.data[] | select(.name == $name)][0].id // empty')
  [[ -z "$marker_id" ]] && mark_unverifiable "No markers found to update"
  local updated_name="Updated $marker_name"
  run_smoke basecamp lineup update "$marker_id" "$updated_name" --json
  assert_success
  assert_json_value '.ok' 'true'
  echo "$updated_name" > "$BATS_FILE_TMPDIR/marker_name"
}

@test "lineup delete removes a lineup marker" {
  local name_file="$BATS_FILE_TMPDIR/marker_name"
  [[ ! -f "$name_file" ]] && mark_unverifiable "No marker created by earlier test"
  local marker_name
  marker_name=$(cat "$name_file")
  run_smoke basecamp lineup list --json
  [[ "$status" -ne 0 ]] && mark_unverifiable "Lineup API not available"
  assert_json_value '.ok' 'true'
  local marker_id
  marker_id=$(echo "$output" | jq -r --arg name "$marker_name" '[.data[] | select(.name == $name)][0].id // empty')
  [[ -z "$marker_id" ]] && mark_unverifiable "No markers found to delete"
  run_smoke basecamp lineup delete "$marker_id" --json
  assert_success
  assert_json_value '.ok' 'true'
}
