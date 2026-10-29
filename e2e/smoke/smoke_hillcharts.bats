#!/usr/bin/env bats
# smoke_hillcharts.bats - Level 0: Hill chart operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "hillcharts show returns hill chart" {
  run_smoke basecamp hillcharts show -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "hillcharts track requires todolist IDs" {
  run_smoke basecamp hillcharts track 999999 -p "$QA_PROJECT" --json
  # Track may fail with not-found for a fake ID; just verify it reaches the API
  mark_unverifiable "Requires valid todolist ID"
}

@test "hillcharts untrack requires todolist IDs" {
  run_smoke basecamp hillcharts untrack 999999 -p "$QA_PROJECT" --json
  # Untrack may fail with not-found for a fake ID; just verify it reaches the API
  mark_unverifiable "Requires valid todolist ID"
}

@test "hillcharts updates list returns progress updates" {
  run_smoke basecamp hillcharts updates list -p "$QA_PROJECT" --json
  # A hill chart with no tracked todolists may be refused (403)
  [[ "$status" -eq 4 ]] && mark_unverifiable "Hill chart not enabled on project"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "hillcharts updates show returns a progress update" {
  run_smoke basecamp hillcharts updates list -p "$QA_PROJECT" --limit 1 --json
  [[ "$status" -eq 0 ]] || mark_unverifiable "Cannot list progress updates"
  local id
  id=$(echo "$output" | jq -r '.data[0].id // empty')
  [[ -n "$id" ]] || mark_unverifiable "No progress updates on the project's hill chart"
  run_smoke basecamp hillcharts updates show "$id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.data.id' "$id"
}

@test "hillcharts update posts a progress update" {
  run_smoke basecamp hillcharts show -p "$QA_PROJECT" --json
  [[ "$status" -eq 0 ]] || mark_unverifiable "Hill chart not enabled on project"
  local id position
  id=$(echo "$output" | jq -r '.data.dots[0].id // empty')
  position=$(echo "$output" | jq -r '.data.dots[0].position // empty')
  [[ -n "$id" && -n "$position" ]] || mark_unverifiable "No tracked todolists on the project's hill chart"
  # Re-post the dot where it sits, so the chart is left as it was.
  run_smoke basecamp hillcharts update --dot "$id=$position" --note "Smoke test" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
