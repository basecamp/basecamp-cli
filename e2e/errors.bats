#!/usr/bin/env bats
# errors.bats - Test error handling

load test_helper


# Flag parsing errors

@test "todos --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todos --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "todos --list is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --list was removed; todolist is set via config or interactive selection
  run basecamp todos --list
  assert_failure
  assert_output_contains "Unknown option"
}

@test "todos --assignee is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --assignee was removed; use basecamp reports assigned instead
  run basecamp todos --assignee
  assert_failure
  assert_output_contains "Unknown option"
}

@test "campfire --campfire without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp campfire messages --campfire
  assert_failure
  assert_output_contains "--campfire requires a value"
}

@test "comment without recording ID shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp comment
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "cards --column is not a valid flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # --column was removed; use 'basecamp cards column' subcommand instead
  run basecamp cards --column
  assert_failure
  assert_output_contains "Unknown option"
}

@test "recordings --type without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings --type
  assert_failure
  assert_output_contains "--type requires a value"
}

@test "recordings --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Global flag errors

@test "basecamp --project without value shows error" {
  create_credentials

  run basecamp --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "basecamp --account without value shows error" {
  create_credentials

  run basecamp --account
  assert_failure
  assert_output_contains "--account requires a value"
}

@test "basecamp --cache-dir without value shows error" {
  create_credentials

  run basecamp --cache-dir
  assert_failure
  assert_output_contains "--cache-dir requires a value"
}


# Missing content errors

@test "todo create without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos create
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "comment without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp comment 123
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "campfire post without message shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp campfire post
  assert_failure
  assert_json_value '.error' '<message> required'
  assert_json_value '.code' 'usage'
}


# Missing context errors

@test "todos without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command without project shows help with subcommand list
  run basecamp todos
  assert_success
  assert_output_contains "COMMANDS"
}

@test "cards without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command without project shows help with subcommand list
  run basecamp cards
  assert_success
  assert_output_contains "COMMANDS"
}

@test "recordings without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp recordings
  assert_failure
  assert_json_value '.error' '<type> required'
  assert_json_value '.code' 'usage'
}


# Show command argument parsing

@test "show handles flags before positional" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Should not treat --project value as ID
  run basecamp show --project 123 todo 456
  # Will fail on API call, but should parse correctly (not "Invalid assignee")
  assert_output_not_contains "Unknown option"
}

@test "todolists show handles --in flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todolists show --in 123 456
  # Will fail on API call, but should parse correctly
  assert_output_not_contains "Unknown option"
}

@test "show --help lists card-table type" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp show --help
  assert_success
  assert_output_contains "card-table"
}

@test "show unknown type error mentions card-table" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp show foobar 456
  assert_failure
  assert_output_contains "Unknown type: foobar"
  assert_output_contains "card-table"
}

@test "show card-table parses type correctly" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Will fail on API call (no project), but should parse card-table type correctly
  run basecamp show card-table 456 --project 123
  assert_output_not_contains "Unknown type"
}

# Assignee validation

@test "email assignee is passed to resolver" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123, "todolist_id": 456}'

  # Email assignees are valid input and passed to ResolvePerson
  # With a fake account, this will fail on API call (not input validation)
  run basecamp todo "test" --assignee "john@example.com"
  assert_failure
  # Should NOT fail with "Invalid assignee" - emails are valid
  assert_output_not_contains "Invalid assignee"
}

@test "search without query shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp search
  assert_failure
  assert_json_value '.error' '<query> required'
  assert_json_value '.code' 'usage'
}

@test "reopen without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp reopen
  assert_failure
  assert_json_value '.error' '<id|url>... required'
  assert_json_value '.code' 'usage'
}

@test "todos position without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos position --to 1
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "todos position without --to shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp todos position 123
  assert_failure
  assert_output_contains "required"
}

@test "comments without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # Group command (no RunE) shows subcommand help
  run basecamp comments
  assert_success
  assert_output_contains "COMMANDS"
}

@test "comments show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments show
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "comments update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments update
  assert_failure
  assert_json_value '.error' '<id|url> required'
  assert_json_value '.code' 'usage'
}

@test "comments update without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp comments update 123
  assert_failure
  assert_json_value '.error' '<content> required'
  assert_json_value '.code' 'usage'
}

@test "messages without subcommand shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command (no RunE) shows subcommand help
  run basecamp messages
  assert_success
  assert_output_contains "COMMANDS"
}

@test "message without subject shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp message
  assert_failure
  assert_json_value '.error' '<title> required'
  assert_json_value '.code' 'usage'
}

# Search JSON cleanliness

@test "search --json outputs clean JSON to stdout" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # The info messages should go to stderr, stdout should be empty or JSON only
  run bash -c "basecamp search todos --json 2>/dev/null"
  # If there's output, it should be valid JSON (starts with { or [)
  if [[ -n "$output" ]]; then
    assert_output_starts_with '{'
  fi
}


# JSON error envelope structure

@test "error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todos --project
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
