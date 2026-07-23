#!/usr/bin/env bats
# installer.bats - Tests for the install scripts' post-install agent setup.
#
# #528: the non-TTY and skip branches must run `setup agents` (baseline skill +
# best-effort agent connection), never the hardcoded `setup claude`. Old release
# binaries (which lack `setup agents`) must fall back without reintroducing the
# Claude-first bug.

setup() {
  # The installer contract keys off these; a leaked value would skew results.
  unset BASECAMP_SKIP_SETUP BASECAMP_SETUP_AGENT

  INSTALL_SH="${BATS_TEST_DIRNAME}/../scripts/install.sh"
  INSTALL_PS1="${BATS_TEST_DIRNAME}/../scripts/install.ps1"

  STUB_DIR="$(mktemp -d)"
  LOG="$STUB_DIR/calls.log"
  write_stub new  # default: a binary that supports `setup agents`
}

teardown() {
  [[ -n "${STUB_DIR:-}" ]] && rm -rf "$STUB_DIR"
}

# write_stub emits a `basecamp` stub that logs its argv. mode=new advertises the
# `setup agents` subcommand in `setup --help`; mode=old omits it and fails an
# actual `setup agents` invocation, mimicking a pre-v0.7.3 release binary.
write_stub() {
  local mode="$1"
  {
    echo '#!/usr/bin/env bash'
    echo "echo \"\$@\" >> \"$LOG\""
    echo 'if [[ "$1 $2" == "setup --help" ]]; then'
    echo '  echo "  claude  Install the Basecamp plugin for Claude Code"'
    if [[ "$mode" == "new" ]]; then
      echo '  echo "  agents  Install the Basecamp skill and connect detected coding agents"'
    fi
    echo '  exit 0'
    echo 'fi'
    if [[ "$mode" == "old" ]]; then
      echo 'if [[ "$1 $2" == "setup agents" ]]; then echo "unknown command \"agents\"" >&2; exit 1; fi'
    fi
    echo 'exit 0'
  } > "$STUB_DIR/basecamp"
  chmod +x "$STUB_DIR/basecamp"
}

run_post_install_setup() {
  run bash -c "
    set -euo pipefail
    ${1:-}
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    post_install_setup basecamp
    cat '$LOG'
  "
}

# The if-form guard must let sourcing define functions without running main.
@test "install.sh can be sourced without running the installer" {
  run bash -c "set -euo pipefail; source '$INSTALL_SH'; echo sourced-ok"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"sourced-ok"* ]]
  [[ "$output" != *"Basecamp CLI"* ]]  # banner would print if main ran
}

@test "new binary: post_install_setup dispatches to 'setup agents', never 'setup claude'" {
  run_post_install_setup
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup agents"* ]]
  [[ "$output" != *"setup claude"* ]]
}

@test "new binary: BASECAMP_SKIP_SETUP path still runs 'setup agents'" {
  run_post_install_setup "export BASECAMP_SKIP_SETUP=1"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup agents"* ]]
  [[ "$output" != *"setup claude"* ]]
}

# Cross-version regression: an old release binary (no `setup agents`) must NOT
# silently fall back to Claude when the selector is unset — it installs the
# shared skill only.
@test "old binary + unset selector falls back to 'skill install', never 'setup claude'" {
  write_stub old
  run_post_install_setup
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"skill install"* ]]
  [[ "$output" != *"setup claude"* ]]
  [[ "$output" != *"setup agents"$'\n'* ]]  # the unknown command is never left as the outcome
}

@test "old binary + BASECAMP_SETUP_AGENT=claude connects claude explicitly" {
  write_stub old
  run_post_install_setup "export BASECAMP_SETUP_AGENT=claude"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup claude"* ]]
  [[ "$output" != *"skill install"* ]]
}

# Explicit `all` intent must dispatch every per-agent setup the old binary
# supports (here the stub advertises only `claude`), never collapse to skill-only.
@test "old binary + BASECAMP_SETUP_AGENT=all runs the supported per-agent setups" {
  write_stub old
  run_post_install_setup "export BASECAMP_SETUP_AGENT=all"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup claude"* ]]
}

@test "install.sh has no residual 'setup claude' dispatch" {
  # `setup claude` may appear only inside the explicit-selector fallback case.
  run grep -n 'setup claude' "$INSTALL_SH"
  [[ "$status" -ne 0 ]]  # no literal `setup claude` string in the script
}

@test "install.sh skip and non-tty branches both dispatch via post_install_setup" {
  run grep -c 'post_install_setup "\$binary_name"' "$INSTALL_SH"
  [[ "$status" -eq 0 ]]
  [[ "$output" -ge 2 ]]
}

@test "install.ps1 routes both branches through the guarded best-effort helper" {
  run grep -c 'Invoke-PostInstallSetup \$installedBinary' "$INSTALL_PS1"
  [[ "$status" -eq 0 ]]
  [[ "$output" -eq 2 ]]
}

@test "install.ps1 helper is guarded and cross-version aware" {
  grep -q 'function Invoke-PostInstallSetup' "$INSTALL_PS1"
  grep -q 'setup agents' "$INSTALL_PS1"
  grep -q 'skill install' "$INSTALL_PS1"
  grep -q 'catch {' "$INSTALL_PS1"
}
