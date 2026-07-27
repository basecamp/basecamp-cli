#!/usr/bin/env bats
# comments.bats - Test comments command error handling (thread input validation)

load test_helper


# Help

@test "comments without subcommand shows help" {
  run basecamp comments
  assert_success
  assert_output_contains "COMMANDS"
}


# thread — argument validation

@test "comments thread without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread
  assert_failure
  assert_output_contains "ID required"
}

@test "comments thread with surplus args is rejected" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread 456 789
  assert_failure
  assert_output_contains "accepts 1 arg"
}

@test "comments thread with non-numeric id shows usage error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread abc --json
  assert_failure
  assert_json_value '.error' 'Invalid comment ID'
  assert_json_value '.code' 'usage'
}

@test "comments thread rejects a plain recording URL and points at show" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread "https://3.basecamp.com/99999/buckets/1/todos/123" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'That URL points to a recording, not a comment'
  assert_json_value '.hint | contains("basecamp show")' 'true'
}

@test "comments thread rejects a URL whose account differs from the configured one" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread "https://3.basecamp.com/88888/buckets/1/todos/123#__recording_456" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'URL account 88888 does not match the configured account 99999'
}

@test "comments thread rejects --all with --window" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread 456 --all --window 5 --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' '--all and --window are mutually exclusive'
}

@test "comments thread rejects a non-positive --window" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread 456 --window 0 --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' '--window must be a positive number'
}

@test "comments thread rejects a non-positive id" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread 0 --json
  assert_failure
  assert_json_value '.error' 'Invalid comment ID'
  assert_json_value '.code' 'usage'
}

@test "comments thread rejects an untrusted host in the URL" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments thread "https://evil.example/99999/buckets/1/comments/456" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'refusing untrusted host in URL — expected a Basecamp URL'
}


# show — shared resolver safety (mirrors thread)

@test "comments show rejects a plain recording URL and points at show" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show "https://3.basecamp.com/99999/buckets/1/todos/123" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'That URL points to a recording, not a comment'
  assert_json_value '.hint | contains("basecamp show")' 'true'
}

@test "comments show rejects a URL whose account differs from the configured one" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show "https://3.basecamp.com/88888/buckets/1/todos/123#__recording_456" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'URL account 88888 does not match the configured account 99999'
}

@test "comments show rejects a non-positive id" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show 0 --json
  assert_failure
  assert_json_value '.error' 'Invalid comment ID'
  assert_json_value '.code' 'usage'
}

@test "comments show rejects an untrusted host in the URL" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show "https://evil.example/99999/buckets/1/comments/456" --json
  assert_failure
  assert_json_value '.code' 'usage'
  assert_json_value '.error' 'refusing untrusted host in URL — expected a Basecamp URL'
}


# Help

@test "comments thread --help shows usage" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp comments thread --help
  assert_success
  assert_output_contains "reply"
  assert_output_contains "--window"
  assert_output_contains "--all"
}
