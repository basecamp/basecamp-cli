#!/usr/bin/env bats
# smoke_schedule.bats - Level 0/1: Schedule operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_schedule || return 1
}

@test "schedule info returns schedule" {
  run_smoke basecamp schedule info --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "schedule entries returns entries" {
  run_smoke basecamp schedule entries --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "schedule show returns entry detail" {
  # Discover an entry from the schedule
  local out
  out=$(basecamp schedule entries --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list schedule entries"
    return
  }
  local entry_id
  entry_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$entry_id" ]] || { mark_unverifiable "No schedule entries in project"; return; }

  run_smoke basecamp schedule show "$entry_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
