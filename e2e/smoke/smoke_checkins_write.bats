#!/usr/bin/env bats
# smoke_checkins_write.bats - Level 1: Checkin question and answer mutations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_questionnaire || return 1
}

@test "checkins question create creates a question" {
  run_smoke basecamp checkins question create "Smoke question $(date +%s)?" \
    --questionnaire "$QA_QUESTIONNAIRE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/question_id"
}

@test "checkins question update updates a question" {
  local id_file="$BATS_FILE_TMPDIR/question_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No question created in prior test"
  local qid
  qid=$(<"$id_file")

  run_smoke basecamp checkins question update "$qid" \
    "Updated question $(date +%s)?" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "checkins answer create creates an answer" {
  local id_file="$BATS_FILE_TMPDIR/question_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No question created in prior test"
  local qid
  qid=$(<"$id_file")

  run_smoke basecamp checkins answer create "$qid" \
    "Smoke answer $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/answer_id"
}

@test "checkins answer update updates an answer" {
  local id_file="$BATS_FILE_TMPDIR/answer_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No answer created in prior test"
  local aid
  aid=$(<"$id_file")

  run_smoke basecamp checkins answer update "$aid" \
    "Updated smoke answer $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}
