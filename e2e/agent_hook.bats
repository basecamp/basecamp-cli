#!/usr/bin/env bats
# agent_hook.bats - Tests for the hidden agent-hook plugin commands

load test_helper

setup_extra() {
  export CLAUDE_PLUGIN_DATA="$TEST_TEMP_DIR/plugin-data"
  export GIT_CONFIG_NOSYSTEM=1
  # Git wrappers (e.g. git-ai) write into .git after commit, racing teardown
  export GIT_AI_SKIP_ALL_HOOKS=1

  REPO="$TEST_TEMP_DIR/repo"
  git init -q -b main "$REPO"
  git -C "$REPO" config user.email "agent-hook@example.com"
  git -C "$REPO" config user.name "Agent Hook Test"
  git -C "$REPO" config commit.gpgsign false
  echo one > "$REPO/file.txt"
  git -C "$REPO" add file.txt
  git -C "$REPO" commit -q -m "initial"
}

hook_payload() {
  local event="$1"
  printf '{"session_id":"e2e-session","tool_use_id":"e2e-tool-use","hook_event_name":"%s","cwd":"%s","tool_input":{"command":"git commit -m ship"}}' \
    "$event" "$REPO"
}

@test "agent-hook is hidden from help" {
  run basecamp --help
  assert_success
  ! echo "$output" | grep -q "agent-hook"
}

@test "agent-hook session-start emits hook JSON on stdout" {
  run bash -c 'echo "{}" | basecamp agent-hook session-start'
  assert_success
  is_valid_json
  assert_output_contains "hookSpecificOutput"
  assert_output_contains "SessionStart"
}

@test "agent-hook nudges after a referenced commit" {
  run bash -c "echo '$(hook_payload PreToolUse)' | basecamp agent-hook pre-commit-snapshot"
  assert_success
  [ -z "$output" ]

  echo two >> "$REPO/file.txt"
  git -C "$REPO" add file.txt
  git -C "$REPO" commit -q -m "BC-123 ship it"

  run bash -c "echo '$(hook_payload PostToolUse)' | basecamp agent-hook post-commit"
  assert_success
  is_valid_json
  assert_output_contains "BC-123"
  assert_output_contains "Nothing was posted"
}

@test "agent-hook stays silent when no commit happened" {
  run bash -c "echo '$(hook_payload PreToolUse)' | basecamp agent-hook pre-commit-snapshot"
  assert_success

  run bash -c "echo '$(hook_payload PostToolUse)' | basecamp agent-hook post-commit"
  assert_success
  [ -z "$output" ]
}
