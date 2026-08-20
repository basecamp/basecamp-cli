#!/usr/bin/env bats
# stdin_dash.bats - "-" (read from stdin) support and the tier-2 dash guard.
#
# Tier 1: content inputs accept "-" to read piped stdin. Tier 2: everywhere
# else, a literal "-" combined with piped stdin is a usage error instead of
# silently becoming literal content — except cobra's generated meta commands
# (help, __complete), which are deliberately exempt and covered below. Every
# case resolves locally — usage errors before any request, or a config write —
# so no cassette or server is needed.

load test_helper


# Tier 1 — "-" resolves against stdin

@test "todos create - with empty pipe is a usage error, not an empty todo" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf '' | basecamp todos create - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "empty"
}

@test "comments create - on a TTY-like stdin errors immediately instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # /dev/null is a character device — the TTY stand-in. Must not block.
  run bash -c "basecamp comments create 123 - --json < /dev/null"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_output_contains "nothing is piped"
}

@test "comments create with a bare pipe and no dash teaches the dash" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bash -c "printf 'hello' | basecamp comments create 123 --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.hint | contains("pass \"-\"")' 'true'
}


# Tier 2 — stray literal "-" with a pipe is rejected

@test "projects create - with piped stdin is rejected with the -- escape" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "printf 'x' | basecamp projects create - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error | contains("<name>")' 'true'
  assert_json_value '.hint | contains("after the -- separator")' 'true'
}

@test "a -- - after the separator passes the guard and lands literally" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # config set writes locally — a deterministic success proving the escaped
  # "-" passed the guard and was stored as a literal value.
  run bash -c "cd '$TEST_PROJECT' && printf 'x' | basecamp config set project_id --json -- -"
  assert_success
  assert_json_value '.data.value' '-'

  run bash -c "cd '$TEST_PROJECT' && basecamp config show --json < /dev/null"
  assert_success
  assert_json_value '.data.project_id.value' '-'
}

@test "a piped bare dash at the root is rejected, not silently quick-started" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "printf 'x' | basecamp - --json"
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error | contains("does not read stdin")' 'true'
}

@test "basecamp unknowncmd still reports an unknown command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "basecamp unknowncmd --json < /dev/null"
  assert_failure
  assert_output_contains "unknown command"
}

@test "piped help - is exempt from the dash guard" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bash -c "printf 'x' | basecamp help - --json"
  assert_success
}

@test "piped completion of a flag word is exempt from the dash guard" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # The shell passes the word being completed, so "todos create -<TAB>" runs
  # this. Guarding it would break flag completion everywhere.
  run bash -c "printf 'x' | basecamp __complete todos create -"
  assert_success
  assert_output_contains "--description"
}


# --jq error rendering. Not stdin-specific, but it shares this file's contract:
# exactly one document reaches stdout, so a machine consumer can parse it.

@test "a jq filter that fails partway leaves one document on stdout" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # This filter emits .error, then raises. writeJQ streams results as it
  # produces them, so the first line is already on stdout when it fails —
  # replaying the envelope would append a second, incompatible document.
  run bash -c "basecamp todos create --jq '.error, error(\"stop\")' 2>/dev/null < /dev/null"
  assert_failure
  [ "${#lines[@]}" -eq 1 ]
  assert_output_contains "required"

  # The failure is still reported, on stderr.
  run bash -c "basecamp todos create --jq '.error, error(\"stop\")' 2>&1 >/dev/null < /dev/null"
  assert_output_contains "--jq"
}

@test "an unparseable jq filter still renders an error raised before validation" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # The stray-dash guard fires before --jq is validated, so the envelope would
  # otherwise be rendered through an unparseable filter and print nothing.
  run bash -c "printf 'x' | basecamp --jq '.[invalid' - 2>/dev/null"
  assert_failure
  assert_output_contains "does not read stdin"
}
