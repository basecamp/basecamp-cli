#!/usr/bin/env bats
# smoke_schedule_write.bats - Level 1: Schedule entry mutations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_schedule || return 1
}

@test "schedule create creates a schedule entry" {
  [[ -n "${QA_SCHEDULE:-}" ]] || mark_unverifiable "No schedule in project"

  run_smoke basecamp schedule create "Smoke event $(date +%s)" \
    --starts-at "2030-01-01" --ends-at "2030-01-02" \
    --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/entry_id"
}

@test "schedule update updates a schedule entry" {
  local id_file="$BATS_FILE_TMPDIR/entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No schedule entry created in prior test"
  local eid
  eid=$(<"$id_file")

  run_smoke basecamp schedule update "$eid" \
    --title "Updated event $(date +%s)" \
    --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
