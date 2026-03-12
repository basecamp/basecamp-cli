#!/usr/bin/env bats
# smoke_todolistgroups.bats - Level 0: Todolist groups

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "todolistgroups list returns groups" {
  run_smoke basecamp todolistgroups list --list "$QA_TODOLIST" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'

  # Capture first group for show test (may be empty)
  echo "$output" | jq -r '.data[0].id // empty' > "$BATS_FILE_TMPDIR/group_id"
}

@test "todolistgroups show returns group detail" {
  local id_file="$BATS_FILE_TMPDIR/group_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No group discovered in prior test"
  local group_id
  group_id=$(<"$id_file")
  [[ -n "$group_id" ]] || mark_unverifiable "No todolist groups in project"

  run_smoke basecamp todolistgroups show "$group_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
