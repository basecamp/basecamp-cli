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

@test "schedule create saves a draft only when requested" {
  local mode expected entry_id
  local flags=()
  for mode in draft default false; do
    flags=()
    expected=active
    if [[ "$mode" == draft ]]; then
      flags=(--draft)
      expected=drafted
    elif [[ "$mode" == false ]]; then
      flags=(--draft=false)
    fi
    run_smoke basecamp schedule create "CLI smoke publication $mode $(date +%s)" \
      --starts-at '2026-11-19T14:00:00Z' --ends-at '2026-11-19T14:30:00Z' \
      --schedule "$QA_SCHEDULE" -p "$QA_PROJECT" --no-subscribe --json "${flags[@]}"
    assert_success
    assert_json_value '.data.status' "$expected"
    assert_json_not_null '.data.app_url'
    entry_id=$(echo "$output" | jq -r '.data.id')
    [[ "$mode" != draft ]] || assert_output_contains 'edit and publish'

    run_smoke basecamp schedule show "$entry_id" --no-comments --json
    assert_success
    assert_json_value '.data.id' "$entry_id"
    assert_json_value '.data.status' "$expected"
  done
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

@test "schedule copy copies a schedule entry onto its own schedule" {
  local id_file="$BATS_FILE_TMPDIR/entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No schedule entry created in prior test"
  local eid
  eid=$(<"$id_file")

  run_smoke basecamp schedule copy "$eid" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.source_id' "$eid"
  assert_json_not_null '.data.id'
}

@test "schedule move refuses the schedule the entry is already on" {
  local id_file="$BATS_FILE_TMPDIR/entry_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No schedule entry created in prior test"
  local eid
  eid=$(<"$id_file")

  # A cross-project move would take the QA entry away; the move itself is
  # covered against local bc3.
  run_smoke basecamp schedule move "$eid" --to-schedule "$QA_SCHEDULE" --json
  assert_failure
  assert_json_value '.ok' 'false'
}
