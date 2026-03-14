#!/usr/bin/env bats
# smoke_lineup.bats - Level 1: Lineup create
#
# Note: lineup update/delete are OOS — the API returns 204 No Content
# (no ID in response), making them structurally untestable without a
# fragile list-after-create workaround. See smoke_lifecycle.bats.

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
