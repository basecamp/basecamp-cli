#!/usr/bin/env bats
# url.bats - Tests for Basecamp URL parsing

load test_helper


# Help

@test "basecamp url --help shows help" {
  run basecamp url --help
  assert_success
  assert_output_contains "parse"
}

@test "basecamp url parse --help shows help" {
  run basecamp url parse --help
  assert_success
  assert_output_contains "URL"
}


# Basic parsing

@test "basecamp url parse parses full message URL" {
  run basecamp url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.project_id" "41746046"
  assert_json_value ".data.type" "messages"
  assert_json_value ".data.recording_id" "9478142982"
}

@test "basecamp url parse parses URL with comment fragment" {
  run basecamp url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.project_id" "41746046"
  assert_json_value ".data.type" "messages"
  assert_json_value ".data.recording_id" "9478142982"
  assert_json_value ".data.comment_id" "9488783598"
}

@test "basecamp url shorthand works without parse subcommand" {
  run basecamp url "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
}


# Different recording types

@test "basecamp url parse parses todo URL" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/todos/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "todos"
  assert_json_value ".data.type_singular" "todo"
  assert_json_value ".data.recording_id" "789"
}

@test "basecamp url parse parses todolist URL" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/todolists/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "todolists"
  assert_json_value ".data.type_singular" "todolist"
}

@test "basecamp url parse parses document URL" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/documents/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "documents"
  assert_json_value ".data.type_singular" "document"
}

@test "basecamp url parse parses campfire URL" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/chats/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "chats"
  assert_json_value ".data.type_singular" "campfire"
}

@test "basecamp url parse parses card URL with nested path" {
  run basecamp url parse "https://3.basecamp.com/2914079/buckets/27/card_tables/cards/9486682178#__recording_9500689518" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.project_id" "27"
  assert_json_value ".data.type" "cards"
  assert_json_value ".data.type_singular" "card"
  assert_json_value ".data.recording_id" "9486682178"
  assert_json_value ".data.comment_id" "9500689518"
}


# Project URLs

@test "basecamp url parse parses project URL" {
  run basecamp url parse "https://3.basecamp.com/2914079/projects/41746046" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.project_id" "41746046"
  assert_json_value ".data.type" "project"
}


# Type list URLs

@test "basecamp url parse parses type list URL" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/todos" --json
  assert_success
  is_valid_json
  assert_json_value ".data.project_id" "456"
  assert_json_value ".data.type" "todos"
  assert_json_value ".data.recording_id" "null"
}


# Error cases

@test "basecamp url parse fails without URL" {
  run basecamp url parse
  assert_failure
  # Go returns generic "ID required" for missing args, Bash returned "URL required"
  assert_output_contains "ID required"
}

@test "basecamp url parse fails for non-Basecamp URL" {
  run basecamp url parse "https://github.com/test/repo"
  assert_failure
  assert_output_contains "Not a Basecamp URL"
}


# Summary output

@test "basecamp url parse has correct summary for message with comment" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111" --json
  assert_success
  assert_json_value ".summary" "Message #789 in project #456, comment #111"
}

@test "basecamp url parse has correct summary for todo" {
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/todos/789" --json
  assert_success
  assert_json_value ".summary" "Todo #789 in project #456"
}


# Breadcrumbs

@test "basecamp url parse includes useful breadcrumbs" {
  skip "breadcrumbs not yet implemented"
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/messages/789" --json
  assert_success
  is_valid_json

  # Should have show, comment, comments breadcrumbs
  local breadcrumb_count
  breadcrumb_count=$(echo "$output" | jq '.breadcrumbs | length')
  [[ "$breadcrumb_count" -ge 3 ]]
}

@test "basecamp url parse includes comment breadcrumb when comment_id present" {
  skip "breadcrumbs not yet implemented"
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111" --json
  assert_success
  is_valid_json

  # Should have show-comment breadcrumb
  local has_show_comment
  has_show_comment=$(echo "$output" | jq '.breadcrumbs[] | select(.action == "show-comment") | .action')
  [[ -n "$has_show_comment" ]]
}


# Markdown output

@test "basecamp url parse shows markdown by default in TTY" {
  # Since bats runs non-TTY, force --md
  run basecamp url parse "https://3.basecamp.com/123/buckets/456/messages/789" --md
  assert_success
  # Check for styled terminal output (key-value pairs with humanized headers)
  assert_output_contains "Type"
  assert_output_contains "message"
}
