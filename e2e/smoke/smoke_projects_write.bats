#!/usr/bin/env bats
# smoke_projects_write.bats - Level 2: Project CRUD lifecycle (serial)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "projects create creates a project" {
  run_smoke basecamp projects create "Smoke project $(date +%s)" \
    --description "Automated smoke test project" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/created_project_id"
}

@test "projects update updates a project" {
  local id_file="$BATS_FILE_TMPDIR/created_project_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No project created in prior test"
  local proj_id
  proj_id=$(<"$id_file")

  run_smoke basecamp projects update "$proj_id" \
    --name "Smoke updated $(date +%s)" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "projects delete deletes a project" {
  local id_file="$BATS_FILE_TMPDIR/created_project_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No project created in prior test"
  local proj_id
  proj_id=$(<"$id_file")

  run_smoke basecamp projects delete "$proj_id" --json
  assert_success
  assert_json_value '.ok' 'true'
}
