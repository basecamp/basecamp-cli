#!/usr/bin/env bats
# smoke_reports.bats - Level 0: Reports, timeline, timesheet, events, show, url

load smoke_helper

setup_file() {
  ensure_token || return 1
}

# --- Reports ---

@test "reports assigned returns assignments" {
  run_smoke basecamp reports assigned --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "reports overdue returns overdue items" {
  run_smoke basecamp reports overdue --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "reports schedule returns schedule entries" {
  run_smoke basecamp reports schedule --json
  # 400 on some dev environments where schedule reports aren't configured
  [[ "$status" -eq 7 ]] && mark_unverifiable "Schedule reports not available in this environment"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "reports assignable returns assignable people" {
  run_smoke basecamp reports assignable --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Timeline ---

@test "timeline returns recent activity" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp timeline -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Timesheet ---

@test "timesheet report returns timesheet data" {
  run_smoke basecamp timesheet report --json
  # Timesheets may not be enabled on all accounts (403 forbidden)
  [[ "$status" -eq 4 ]] && mark_unverifiable "Timesheets not enabled in this account"
  assert_success
  assert_json_value '.ok' 'true'
}

@test "timesheet project returns project timesheet" {
  ensure_project || mark_unverifiable "Cannot discover project"
  run_smoke basecamp timesheet project -p "$QA_PROJECT" --json
  [[ "$status" -eq 4 ]] && mark_unverifiable "Timesheets not enabled in this account"
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Events ---

@test "events returns event timeline for a recording" {
  ensure_project || mark_unverifiable "Cannot discover project"
  ensure_todo || mark_unverifiable "Cannot discover todo"
  run_smoke basecamp events "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Show (generic) ---

@test "show displays a recording by type and id" {
  ensure_project || mark_unverifiable "Cannot discover project"
  ensure_todo || mark_unverifiable "Cannot discover todo"
  run_smoke basecamp show --type todo "$QA_TODO" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

# --- URL ---

@test "url parse extracts components from a basecamp URL" {
  run_smoke basecamp url parse "https://3.basecamp.com/1234/buckets/5678/todos/9012"
  assert_success
  assert_output_contains "9012"
}

# --- Profile ---

@test "profile list returns profiles" {
  run_smoke basecamp profile list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "profile show returns profile detail" {
  # profile show requires a configured profile; without one it shows help
  run_smoke basecamp profile show --json
  # May return help (exit 0) if no profile is configured in temp HOME
  if echo "$output" | jq -e '.ok' >/dev/null 2>&1; then
    assert_json_value '.ok' 'true'
  else
    mark_unverifiable "No profile configured in smoke environment"
  fi
}
