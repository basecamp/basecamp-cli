#!/usr/bin/env bats
# agent_hook.bats - Tests for the hidden agent-hook plugin commands

load test_helper

setup_extra() {
  export CLAUDE_PLUGIN_DATA="$TEST_TEMP_DIR/plugin-data"
  export GIT_CONFIG_NOSYSTEM=1
  export GIT_CONFIG_GLOBAL=/dev/null
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

assert_hook_context_contains() {
  local needle="$1"
  local context
  context=$(echo "$output" | jq -r '.hookSpecificOutput.additionalContext')
  if [[ "$context" != *"$needle"* ]]; then
    echo "additionalContext missing '$needle': $context"
    return 1
  fi
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
  assert_json_value ".hookSpecificOutput.hookEventName" "SessionStart"
  assert_json_not_null ".hookSpecificOutput.additionalContext"
  assert_hook_context_contains "Basecamp is active"
}

@test "agent-hook tolerates invalid config (exit 0, still emits)" {
  # Non-localhost http base_url fails the root command's HTTPS enforcement
  # with exit 7 — the hook lifecycle must bypass that and stay non-blocking.
  run bash -c 'echo "{}" | BASECAMP_BASE_URL=http://example.test basecamp agent-hook session-start'
  assert_success
  is_valid_json
  assert_json_value ".hookSpecificOutput.hookEventName" "SessionStart"
}

@test "agent-hook tolerates multiple profiles without a default (exit 0)" {
  # Multiple profiles and no default_profile make the root lifecycle fail
  # with a profile-resolution error — hooks must not inherit that.
  cat > "$TEST_HOME/.config/basecamp/config.json" <<'EOF'
{
  "profiles": {
    "work": {"base_url": "https://3.basecampapi.com", "account_id": "111"},
    "personal": {"base_url": "https://3.basecampapi.com", "account_id": "222"}
  }
}
EOF
  run bash -c 'echo "{}" | basecamp agent-hook session-start'
  assert_success
  is_valid_json
  assert_json_value ".hookSpecificOutput.hookEventName" "SessionStart"
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
  assert_json_value ".hookSpecificOutput.hookEventName" "PostToolUse"
  assert_hook_context_contains "BC-123"
  assert_hook_context_contains "Nothing was posted"
}

@test "agent-hook stays silent when no commit happened" {
  run bash -c "echo '$(hook_payload PreToolUse)' | basecamp agent-hook pre-commit-snapshot"
  assert_success

  run bash -c "echo '$(hook_payload PostToolUse)' | basecamp agent-hook post-commit"
  assert_success
  [ -z "$output" ]
}
