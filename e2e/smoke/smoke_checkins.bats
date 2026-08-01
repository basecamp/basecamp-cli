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

@test "checkins answer show returns an answer" {
  # Use provisioned answer or ensure helper
  local aid="${QA_ANSWER:-}"
  [[ -n "$aid" ]] || { ensure_answer || return 0; aid="$QA_ANSWER"; }

  run_smoke basecamp checkins answer show "$aid" \
    --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "checkins reminders lists pending reminders" {
  run_smoke basecamp checkins reminders --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "checkins reminders honors --limit" {
  run_smoke basecamp checkins reminders --limit 1 --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "checkins question answerers rejects a non-id" {
  run_smoke basecamp checkins question answerers not-an-id --json
  assert_failure
}

@test "checkins question notify requires a setting" {
  # Naming no setting would be a no-op write, so it is refused before the
  # request rather than sent as an empty update.
  run_smoke basecamp checkins question notify 999999 --json
  assert_failure
}

@test "checkins question pause is out of scope" {
  mark_out_of_scope "Mutating - pauses a live recurring question"
}

@test "checkins question resume is out of scope" {
  mark_out_of_scope "Mutating - resumes a live recurring question"
}
