#!/usr/bin/env bats
# setup.bats - `basecamp setup` refuses to prompt when nothing can answer it.
#
# The wizard is prompts end to end, and huh runs them as a bubbletea program.
# Redirecting stdin does not make that program fail: bubbletea sees a
# non-terminal stdin and opens /dev/tty instead, so the prompt waits on the real
# terminal — `basecamp setup --json < /dev/null` hung forever.
#
# Every case runs under a timeout, and the timeout is the assertion: exit 124 is
# the bug reproducing. A unit test with a fake reader cannot catch this, because
# the hang lives in what the real os.Stdin makes bubbletea do — which is also
# why the PTY case at the bottom exists. Under bats alone, stdout is captured,
# so stdout is what fails the interactivity check and stdin is never the
# deciding factor.

load test_helper

# timeout_bin names GNU timeout, which stock macOS does not ship. Tests that
# need it skip rather than silently drop their only real assertion.
timeout_bin() {
  if command -v timeout >/dev/null 2>&1; then
    echo timeout
  elif command -v gtimeout >/dev/null 2>&1; then
    echo gtimeout
  fi
}

# run_guarded runs a shell snippet under a timeout.
#
# The timeout wraps the whole shell, not the snippet's first word. Prefixing it
# (`timeout 10 printf '' | basecamp setup`) times `printf` and leaves `basecamp`
# to hang the suite forever — the exact failure these tests exist to catch.
# pipefail so a refusal upstream of a pipe is not masked by the last stage.
run_guarded() {
  local to
  to="$(timeout_bin)"
  if [[ -z "$to" ]]; then
    skip "no GNU timeout available (install coreutils)"
  fi
  run "$to" 10 bash -c "set -o pipefail; $1"
}

assert_not_timed_out() {
  if [[ "$status" -eq 124 ]]; then
    echo "Command hit the timeout (exit 124) — it hung instead of refusing"
    echo "Output: $output"
    return 1
  fi
}

# assert_refused is the whole contract: exit 1, a usage envelope, and a hint
# naming something that works without a terminal. "Non-zero and not 124" is too
# weak — the pre-fix binary also exits non-zero where no controlling terminal
# exists, on huh's own TTY error, several steps in.
assert_refused() {
  assert_not_timed_out
  assert_exit_code 1
  assert_json_value '.code' 'usage'
  assert_json_value '.hint | contains("basecamp setup agents")' 'true'
}


@test "setup --json with stdin closed refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run_guarded "basecamp setup --json < /dev/null"
  assert_refused
}

@test "setup without --json and stdin closed refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run_guarded "basecamp setup < /dev/null"
  assert_refused
}

@test "setup with piped stdin refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run_guarded "printf '' | basecamp setup --json"
  assert_refused
}

@test "setup with piped stdout refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run_guarded "basecamp setup --json < /dev/null | cat"
  assert_refused
}


# The reported bug: a real terminal on stdout, nothing on stdin. bats cannot
# produce that on its own — it captures stdout — so borrow a pty from script(1).
# The exit code travels in a sentinel rather than through script, whose status
# propagation differs between the util-linux and BSD versions.

# run_in_pty runs a shell snippet with stdout attached to a pseudo-terminal.
run_in_pty() {
  local snippet="$1"
  if ! command -v script >/dev/null 2>&1; then
    skip "script(1) not available to allocate a pty"
  fi
  if script --version 2>&1 | grep -qi util-linux; then
    run bash -c "script -qec '$snippet' /dev/null | tr -d '\r'"
  else
    run bash -c "script -q /dev/null /bin/sh -c '$snippet' | tr -d '\r'"
  fi
}

@test "setup --json on a terminal with stdin closed refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  local to
  to="$(timeout_bin)"
  if [[ -z "$to" ]]; then
    skip "no GNU timeout available (install coreutils)"
  fi

  # stdout is a pty here, so /dev/null on stdin is the only disqualifier — the
  # case a character-device check called interactive and bubbletea did not.
  run_in_pty "$to 10 basecamp setup --json < /dev/null; echo EXIT:\$?"

  assert_output_not_contains "EXIT:124"
  assert_output_contains "EXIT:1"
  assert_output_contains "basecamp setup agents"
}

@test "setup on a terminal with stdin closed refuses instead of hanging" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  local to
  to="$(timeout_bin)"
  if [[ -z "$to" ]]; then
    skip "no GNU timeout available (install coreutils)"
  fi

  run_in_pty "$to 10 basecamp setup < /dev/null; echo EXIT:\$?"

  assert_output_not_contains "EXIT:124"
  assert_output_contains "EXIT:1"
  assert_output_contains "basecamp setup agents"
}


# The gate is on the parent's RunE only. These three are the supported
# non-interactive paths and have to keep working — a persistent hook would have
# taken all of them out, which is the easiest thing to get wrong here.

# hide_agent_binaries drops the developer's real claude/codex from PATH. Without
# it these tests shell out to whichever agent CLI happens to be installed, and
# those have prompts of their own — a hang in somebody else's tool, unrelated to
# the gate under test. What we are asserting is that the parent's gate does not
# reach the subcommands, and that holds with or without an agent installed.
hide_agent_binaries() {
  export PATH="$BASECAMP_ROOT/bin:/usr/bin:/bin"
}

@test "setup agents still runs without a terminal" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  hide_agent_binaries
  export BASECAMP_SETUP_AGENT=none

  run_guarded "basecamp setup agents --json < /dev/null"
  assert_not_timed_out
  assert_success
}

@test "setup claude still runs without a terminal" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  hide_agent_binaries

  run_guarded "basecamp setup claude --json < /dev/null"
  assert_not_timed_out
  assert_success
}

@test "setup codex still runs without a terminal" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  hide_agent_binaries

  run_guarded "basecamp setup codex --json < /dev/null"
  assert_not_timed_out
  assert_success
}
