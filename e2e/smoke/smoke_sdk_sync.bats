#!/usr/bin/env bats
# smoke_sdk_sync.bats - SDK sync command coverage

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
  ensure_project || return 1
}

@test "accounts show returns account detail" {
  run_smoke basecamp accounts show --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "accounts rename is out of scope" {
  mark_out_of_scope "Renaming the shared smoke-test account is not safe for routine smoke coverage"
}

@test "accounts logo set is out of scope" {
  mark_out_of_scope "Uploading a shared account logo mutates global account state"
}

@test "accounts logo remove is out of scope" {
  mark_out_of_scope "Removing a shared account logo mutates global account state"
}

@test "gauges list returns gauges" {
  run_smoke basecamp gauges list --json
  [[ "$status" -ne 0 ]] && mark_unverifiable "Gauges are not available in this environment"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "gauges needles returns project gauge needles" {
  run_smoke basecamp gauges needles -p "$QA_PROJECT" --json
  [[ "$status" -ne 0 ]] && mark_unverifiable "Project gauge needles are not available in this environment"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "gauges show returns gauge needle detail" {
  local out
  out=$(basecamp gauges needles -p "$QA_PROJECT" --json 2>/dev/null) || mark_unverifiable "Cannot list gauge needles"
  local needle_id
  needle_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$needle_id" ]] || mark_unverifiable "No gauge needles found"

  run_smoke basecamp gauges show "$needle_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "gauges create" {
  mark_unverifiable "Gauge write smoke coverage needs a dedicated writable gauge fixture"
}

@test "gauges update" {
  mark_unverifiable "Gauge write smoke coverage needs a dedicated writable gauge fixture"
}

@test "gauges delete" {
  mark_unverifiable "Gauge write smoke coverage needs a dedicated writable gauge fixture"
}

@test "gauges enable" {
  mark_unverifiable "Gauge toggle smoke coverage needs a dedicated writable project fixture"
}

@test "gauges disable" {
  mark_unverifiable "Gauge toggle smoke coverage needs a dedicated writable project fixture"
}

@test "notifications list returns notification groups" {
  run_smoke basecamp notifications list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "notifications read marks a notification as read" {
  local out
  out=$(basecamp notifications list --json 2>/dev/null) || mark_unverifiable "Cannot list notifications"
  local readable_sgid
  readable_sgid=$(echo "$out" | jq -r '.data.unreads[0].readable_sgid // empty')
  [[ -n "$readable_sgid" ]] || mark_unverifiable "No unread notifications available to mark read"

  run_smoke basecamp notifications read "$readable_sgid" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people profile show returns profile detail" {
  run_smoke basecamp people profile show --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "people profile update round-trips the current name" {
  local out
  out=$(basecamp people profile show --json 2>/dev/null) || mark_unverifiable "Cannot show current profile"
  local current_name
  current_name=$(echo "$out" | jq -r '.data.name // empty')
  [[ -n "$current_name" ]] || mark_unverifiable "Profile name is empty"

  run_smoke basecamp people profile update --name "$current_name" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people preferences show returns preferences" {
  run_smoke basecamp people preferences show --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people preferences update round-trips current preferences" {
  local out
  out=$(basecamp people preferences show --json 2>/dev/null) || mark_unverifiable "Cannot show current preferences"
  local time_format
  local time_zone
  time_format=$(echo "$out" | jq -r '.data.time_format // empty')
  time_zone=$(echo "$out" | jq -r '.data.time_zone_name // empty')
  [[ -n "$time_format" ]] || mark_unverifiable "Time format is empty"
  [[ -n "$time_zone" ]] || mark_unverifiable "Time zone is empty"

  run_smoke basecamp people preferences update \
    --time-format "$time_format" \
    --time-zone "$time_zone" \
    --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people out-of-office show returns status" {
  run_smoke basecamp people out-of-office show --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people out-of-office enable is out of scope" {
  mark_out_of_scope "Enabling out-of-office mutates personal availability state"
}

@test "people out-of-office disable is out of scope" {
  mark_out_of_scope "Disabling out-of-office mutates personal availability state"
}

@test "reports mine returns current assignments" {
  run_smoke basecamp reports mine --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "reports completed returns completed assignments" {
  run_smoke basecamp reports completed --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "reports due returns due assignments" {
  run_smoke basecamp reports due --scope overdue --json
  assert_success
  assert_json_value '.ok' 'true'
}
