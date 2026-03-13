#!/usr/bin/env bats
# smoke_todolistgroups_write.bats - Level 1: Todolist group mutations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_todolist || return 1
}

@test "todolistgroups create creates a group" {
  run_smoke basecamp todolistgroups create "Smoke group $(date +%s)" \
    --list "$QA_TODOLIST" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/group_id"
}

@test "todolistgroups update updates a group" {
  local id_file="$BATS_FILE_TMPDIR/group_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No group created in prior test"
  local gid
  gid=$(<"$id_file")

  run_smoke basecamp todolistgroups update "$gid" \
    "Updated group $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "todolistgroups position moves a group" {
  local id_file="$BATS_FILE_TMPDIR/group_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No group created in prior test"
  local gid
  gid=$(<"$id_file")

  run_smoke basecamp todolistgroups position "$gid" --position 1 -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
