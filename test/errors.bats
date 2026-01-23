#!/usr/bin/env bats
# errors.bats - Test error handling

load test_helper


# Flag parsing errors

@test "todos --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "todos --list without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --list
  assert_failure
  assert_output_contains "--list requires a value"
}

@test "todos --assignee without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --assignee
  assert_failure
  assert_output_contains "--assignee requires a value"
}

@test "campfire --campfire without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq campfire messages --campfire
  assert_failure
  assert_output_contains "--campfire requires a value"
}

@test "comment --on without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq comment "test" --on
  assert_failure
  assert_output_contains "--on requires a recording ID"
}

@test "cards --column without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq cards --column
  assert_failure
  assert_output_contains "--column requires a value"
}

@test "recordings --type without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings --type
  assert_failure
  assert_output_contains "--type requires a value"
}

@test "recordings --limit without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings --limit
  assert_failure
  assert_output_contains "--limit requires a value"
}


# Global flag errors

@test "bcq --project without value shows error" {
  create_credentials

  run bcq --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "bcq --account without value shows error" {
  create_credentials

  run bcq --account
  assert_failure
  assert_output_contains "--account requires a value"
}

@test "bcq --cache-dir without value shows error" {
  create_credentials

  run bcq --cache-dir
  assert_failure
  assert_output_contains "--cache-dir requires a value"
}


# Missing content errors

@test "todo create without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq todos create
  assert_failure
  assert_output_contains "Todo content required"
}

@test "comment without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq comment --on 123
  assert_failure
  assert_output_contains "Comment content required"
}

@test "campfire post without message shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq campfire post
  assert_failure
  assert_output_contains "Message content required"
}


# Missing context errors

@test "todos without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos
  assert_failure
  assert_output_contains "No project specified"
}

@test "cards without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq cards
  assert_failure
  assert_output_contains "No project specified"
}

@test "recordings without type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq recordings
  assert_failure
  assert_output_contains "Type required"
}


# Show command argument parsing

@test "show handles flags before positional" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Should not treat --project value as ID
  run bcq show --project 123 todo 456
  # Will fail on API call, but should parse correctly (not "Invalid assignee")
  assert_output_not_contains "Unknown option"
}

@test "todolists show handles --in flag" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todolists show --in 123 456
  # Will fail on API call, but should parse correctly
  assert_output_not_contains "Unknown option"
}

@test "show --help lists card-table type" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq show --help
  assert_success
  assert_output_contains "card-table"
}

@test "show unknown type error mentions card-table" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq show foobar 456
  assert_failure
  assert_output_contains "Unknown type: foobar"
  assert_output_contains "card-table"
}

@test "show card-table parses type correctly" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Will fail on API call (no project), but should parse card-table type correctly
  run bcq show card-table 456 --project 123
  assert_output_not_contains "Unknown type"
}

# Assignee validation

@test "invalid assignee format shows clear error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123, "todolist_id": 456}'

  run bcq todo --content "test" --assignee "john@example.com"
  assert_failure
  assert_output_contains "Invalid assignee"
  assert_output_contains "numeric person ID"
}

@test "search without query shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq search
  assert_failure
  assert_output_contains "Search query required"
}

@test "reopen without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq reopen
  assert_failure
  assert_output_contains "Todo ID(s) required"
}

@test "todos position without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq todos position --to 1
  assert_failure
  assert_output_contains "Todo ID required"
}

@test "todos position without position shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq todos position 123
  assert_failure
  assert_output_contains "Position required"
}

@test "comments list without recording shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq comments
  assert_failure
  assert_output_contains "Recording ID required"
}

@test "comments show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq comments show
  assert_failure
  assert_output_contains "Comment ID required"
}

@test "comments update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq comments update "new content"
  assert_failure
  assert_output_contains "Comment ID required"
}

@test "comments update without content shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq comments update 123
  assert_failure
  assert_output_contains "Content required"
}

@test "messages without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq messages
  assert_failure
  assert_output_contains "No project specified"
}

@test "message without subject shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq message
  assert_failure
  assert_output_contains "Message subject required"
}

# Search JSON cleanliness

@test "search --json outputs clean JSON to stdout" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # The info messages should go to stderr, stdout should be empty or JSON only
  run bash -c "bcq search todos --json 2>/dev/null"
  # If there's output, it should be valid JSON (starts with { or [)
  if [[ -n "$output" ]]; then
    assert_output_starts_with '{'
  fi
}


# JSON error envelope structure

@test "error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq todos --project
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}
