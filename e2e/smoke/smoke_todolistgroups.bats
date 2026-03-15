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
  # Use provisioned group, file-based discovery from prior test, or ensure helper
  local group_id="${QA_TODOLIST_GROUP:-}"
  if [[ -z "$group_id" ]]; then
    local id_file="$BATS_FILE_TMPDIR/group_id"
    [[ -f "$id_file" ]] && group_id=$(<"$id_file")
  fi
  [[ -n "$group_id" ]] || { ensure_todolist_group || return 0; group_id="$QA_TODOLIST_GROUP"; }

  run_smoke basecamp todolistgroups show "$group_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
