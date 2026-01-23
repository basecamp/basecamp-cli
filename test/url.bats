#!/usr/bin/env bats
# url.bats - Tests for Basecamp URL parsing

load test_helper


# Help

@test "bcq url --help shows help" {
  run bcq url --help
  assert_success
  assert_output_contains "parse"
}

@test "bcq url parse --help shows help" {
  run bcq url parse --help
  assert_success
  assert_output_contains "URL"
}


# Basic parsing

@test "bcq url parse parses full message URL" {
  run bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.bucket_id" "41746046"
  assert_json_value ".data.type" "messages"
  assert_json_value ".data.recording_id" "9478142982"
}

@test "bcq url parse parses URL with comment fragment" {
  run bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.bucket_id" "41746046"
  assert_json_value ".data.type" "messages"
  assert_json_value ".data.recording_id" "9478142982"
  assert_json_value ".data.comment_id" "9488783598"
}

@test "bcq url shorthand works without parse subcommand" {
  run bcq url "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
}


# Different recording types

@test "bcq url parse parses todo URL" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/todos/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "todos"
  assert_json_value ".data.type_singular" "todo"
  assert_json_value ".data.recording_id" "789"
}

@test "bcq url parse parses todolist URL" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/todolists/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "todolists"
  assert_json_value ".data.type_singular" "todolist"
}

@test "bcq url parse parses document URL" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/documents/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "documents"
  assert_json_value ".data.type_singular" "document"
}

@test "bcq url parse parses campfire URL" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/chats/789" --json
  assert_success
  is_valid_json
  assert_json_value ".data.type" "chats"
  assert_json_value ".data.type_singular" "campfire"
}


# Project URLs

@test "bcq url parse parses project URL" {
  run bcq url parse "https://3.basecamp.com/2914079/projects/41746046" --json
  assert_success
  is_valid_json
  assert_json_value ".data.account_id" "2914079"
  assert_json_value ".data.bucket_id" "41746046"
  assert_json_value ".data.type" "project"
}


# Type list URLs

@test "bcq url parse parses type list URL" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/todos" --json
  assert_success
  is_valid_json
  assert_json_value ".data.bucket_id" "456"
  assert_json_value ".data.type" "todos"
  assert_json_value ".data.recording_id" "null"
}


# Error cases

@test "bcq url parse fails without URL" {
  run bcq url parse
  assert_failure
  assert_output_contains "URL required"
}

@test "bcq url parse fails for non-Basecamp URL" {
  run bcq url parse "https://github.com/test/repo"
  assert_failure
  assert_output_contains "Not a Basecamp URL"
}


# Summary output

@test "bcq url parse has correct summary for message with comment" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111" --json
  assert_success
  assert_json_value ".summary" "Message #789 in project #456, comment #111"
}

@test "bcq url parse has correct summary for todo" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/todos/789" --json
  assert_success
  assert_json_value ".summary" "Todo #789 in project #456"
}


# Breadcrumbs

@test "bcq url parse includes useful breadcrumbs" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/messages/789" --json
  assert_success
  is_valid_json

  # Should have show, comment, comments breadcrumbs
  local breadcrumb_count
  breadcrumb_count=$(echo "$output" | jq '.breadcrumbs | length')
  [[ "$breadcrumb_count" -ge 3 ]]
}

@test "bcq url parse includes comment breadcrumb when comment_id present" {
  run bcq url parse "https://3.basecamp.com/123/buckets/456/messages/789#__recording_111" --json
  assert_success
  is_valid_json

  # Should have show-comment breadcrumb
  local has_show_comment
  has_show_comment=$(echo "$output" | jq '.breadcrumbs[] | select(.action == "show-comment") | .action')
  [[ -n "$has_show_comment" ]]
}


# Markdown output

@test "bcq url parse shows markdown by default in TTY" {
  # Since bats runs non-TTY, force --md
  run bcq url parse "https://3.basecamp.com/123/buckets/456/messages/789" --md
  assert_success
  assert_output_contains "Parsed URL"
  assert_output_contains "Component"
}
