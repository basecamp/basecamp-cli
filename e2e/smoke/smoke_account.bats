#!/usr/bin/env bats
# smoke_account.bats - Level 2: Account-scoped operations (people, templates)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "people list returns people" {
  run_smoke basecamp people list --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data[0].id'
}

@test "templates list returns templates" {
  run_smoke basecamp templates list --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "people show returns person detail" {
  run_smoke basecamp people show me --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "templates show returns template detail" {
  local out
  out=$(basecamp templates list --json 2>/dev/null) || mark_unverifiable "Cannot list templates"
  local tmpl_id
  tmpl_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$tmpl_id" ]] || mark_unverifiable "No templates found"

  run_smoke basecamp templates show "$tmpl_id" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "people pingable returns pingable people" {
  run_smoke basecamp people pingable --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "auth token shows current token" {
  run_smoke basecamp auth token --json
  assert_success
  assert_json_value '.ok' 'true'
}

# --- Account management ---

@test "accounts show returns account details" {
  run_smoke basecamp accounts show --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "accounts update rejects bare invocation" {
  run_smoke basecamp accounts update --json
  assert_failure
  assert_output_contains "No changes specified"
}

@test "accounts logo upload rejects invalid file" {
  run_smoke basecamp accounts logo upload /dev/null --json
  assert_failure
}

@test "accounts logo remove deletes logo" {
  mark_unverifiable "Mutating test - logo removal not safe in smoke environment"
  run_smoke basecamp accounts logo remove --json
}

# --- account alias (covered by accounts tests above) ---

@test "account is out of scope" {
  mark_out_of_scope "Alias for accounts — tested via canonical form"
}
