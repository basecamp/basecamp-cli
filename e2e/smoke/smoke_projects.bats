#!/usr/bin/env bats
# smoke_projects.bats - Level 0: Projects and accounts
#
# Discovery is minimal: only ensure_project for the show test.
# accounts list and projects list are tested without pre-discovery
# so regressions surface as failures, not unverifiable gaps.

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "accounts list returns accounts" {
  run_smoke basecamp accounts list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data[0].id'
  assert_json_not_null '.data[0].name'
}

@test "projects list returns projects" {
  run_smoke basecamp projects list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data[0].id'
  assert_json_not_null '.data[0].name'
  assert_json_not_null '.summary'

  # Capture a project ID for the show test (within this file only)
  echo "$output" | jq -r '.data[0].id' > "$BATS_FILE_TMPDIR/project_id"
}

@test "projects show returns project detail" {
  local proj_file="$BATS_FILE_TMPDIR/project_id"
  [[ -f "$proj_file" ]] || skip "projects list did not produce a project ID"
  local proj_id
  proj_id=$(<"$proj_file")

  run_smoke basecamp projects show "$proj_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
  assert_json_not_null '.data.name'
}
