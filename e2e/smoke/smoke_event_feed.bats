#!/usr/bin/env bats
# smoke_event_feed.bats - Level 0: Account event feed operations
#
# Nothing here captures a position, a ticket or a cable URL: the first is a
# signed resume token and the other two are bearers, and bats keeps $output —
# a failing assertion prints it in full (test_helper.bash:118) into logs
# somebody keeps. So every read projects the envelope down to the fields it
# asserts with --jq, rather than capturing the whole thing and trusting
# redaction. Entering at the present (--since now) also keeps these reads
# bounded — a since=0 replay would walk the account's served history.
#
# This sits in Level 0 alongside the other read-only suites even though the
# ticket mint is a POST: it writes no record, mints a stateless signed token
# that expires in about two minutes, and leaves nothing behind for a parallel
# suite to trip over.

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "events poll enters the feed at the present" {
  run_smoke basecamp events poll --since now --jq '{ok: .ok}'
  assert_success
  assert_json_value '.ok' 'true'
}

@test "events poll rejects a since that is not an entry point" {
  run_smoke basecamp events poll --since yesterday --json
  assert_failure
  assert_output_contains "Invalid --since"
}

@test "events ticket mints a redacted ticket" {
  run_smoke basecamp events ticket --jq '{ok: .ok, data: {ticket: .data.ticket}}'
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.ticket' '[REDACTED]'
}

@test "inbox polls addressed items" {
  run_smoke basecamp inbox --since now --jq '{ok: .ok, error: .error}'
  # The inbox is served to agent principals only; a person's token gets 403.
  if [[ "$status" -ne 0 ]]; then
    assert_output_contains "agent principals only"
    mark_unverifiable "Inbox is agents-only and this token is not an agent's"
  fi
  assert_json_value '.ok' 'true'
}
