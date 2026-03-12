#!/usr/bin/env bats
# smoke_checkins.bats - Level 0: Checkin questions and answers

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_questionnaire || return 1
}

@test "checkins questions returns questions" {
  run_smoke basecamp checkins questions --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "checkins question show returns a question" {
  # Discover a question from the list
  local out
  out=$(basecamp checkins questions --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list checkin questions"
    return
  }
  local qid
  qid=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$qid" ]] || mark_unverifiable "No checkin questions in project"

  run_smoke basecamp checkins question show "$qid" --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$qid" > "$BATS_FILE_TMPDIR/question_id"
}

@test "checkins answers returns answers for a question" {
  local id_file="$BATS_FILE_TMPDIR/question_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No question discovered in prior test"
  local qid
  qid=$(<"$id_file")

  run_smoke basecamp checkins answers "$qid" --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
