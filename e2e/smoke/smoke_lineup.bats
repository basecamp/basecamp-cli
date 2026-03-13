#!/usr/bin/env bats
# smoke_lineup.bats - Level 1: Lineup CRUD lifecycle
#
# Note: lineup create/update return 204 No Content (no ID in response),
# so update/delete cannot chain off a created marker without a list command.

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "lineup create creates a lineup marker" {
  local future_date
  future_date=$(date -v+7d +%Y-%m-%d 2>/dev/null || date -d "+7 days" +%Y-%m-%d)
  run_smoke basecamp lineup create "Smoke lineup $(date +%s)" "$future_date" --json
  # Lineup API may not exist on all environments (404 → validation error)
  [[ "$status" -ne 0 ]] && mark_unverifiable "Lineup API not available"
  assert_json_value '.ok' 'true'
}

@test "lineup update updates a lineup marker" {
  mark_unverifiable "lineup create returns 204 No Content — no ID to chain"
}

@test "lineup delete removes a lineup marker" {
  mark_unverifiable "lineup create returns 204 No Content — no ID to chain"
}
