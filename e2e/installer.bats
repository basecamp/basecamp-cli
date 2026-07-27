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
      # A pre-`setup agents` binary advertises only `setup claude`. Reject every
      # OTHER `setup <sub>`: the real old parent would swallow it as a stray arg
      # and launch the interactive wizard — the exact bug the installer must avoid.
      echo 'if [[ "$1" == "setup" && "$2" != "claude" && "$2" != "--help" ]]; then echo "unknown command \"$2\"" >&2; exit 1; fi'
    fi
    echo 'exit 0'
  } > "$STUB_DIR/basecamp"
  chmod +x "$STUB_DIR/basecamp"
}

# write_nk_stub emits a stub that logs BASECAMP_NO_KEYRING alongside argv, for
# asserting the escape-hatch belt. Separate from write_stub so the existing
# argv-shape assertions stay byte-exact. mode mirrors write_stub's new/old.
write_nk_stub() {
  local mode="$1"
  {
    echo '#!/usr/bin/env bash'
    echo "echo \"nk=\${BASECAMP_NO_KEYRING:-unset} \$@\" >> \"$LOG\""
    echo 'if [[ "$1 $2" == "setup --help" ]]; then'
    if [[ "$mode" == "new" ]]; then
      echo '  echo "  agents  Install the Basecamp skill and connect detected coding agents"'
    fi
    echo '  exit 0'
    echo 'fi'
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

@test "install.sh runs main when piped to bash" {
  # PATH points at a binary-free tmpdir so main stops at its curl
  # prerequisite without downloading anything or touching the host.
  run bash -c "cat '$INSTALL_SH' | PATH='$BATS_TEST_TMPDIR' '$BASH'"
  [[ "$status" -ne 0 ]]
  [[ "$output" == *"curl is required but not installed"* ]]
  [[ "$output" != *"BASH_SOURCE[0]: unbound variable"* ]]
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
  [[ "$output" != *"setup codex"* ]]  # codex unadvertised → never invoked
}

# Explicit `codex` on an old binary that lacks `setup codex` must NOT run the
# unknown subcommand (which would launch the interactive wizard) — it degrades
# to the shared skill.
@test "old binary + BASECAMP_SETUP_AGENT=codex degrades to 'skill install', never 'setup codex'" {
  write_stub old
  run_post_install_setup "export BASECAMP_SETUP_AGENT=codex"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"skill install"* ]]
  [[ "$output" != *"setup codex"* ]]
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
  # Explicit claude|codex selectors must be capability-checked before dispatch,
  # so an old binary never gets an unadvertised subcommand as a stray arg.
  grep -qF 'match "(?m)^\s+$selector\s"' "$INSTALL_PS1"
  # The keyring escape hatch belt (see the BASECAMP_NO_KEYRING tests below).
  grep -q 'BASECAMP_NO_KEYRING' "$INSTALL_PS1"
}

# Release binaries up to v0.7.2 probe the OS keyring on startup for every
# command, and a locked headless keychain blocks that probe forever (#568
# canary discovery). The installer's best-effort children never touch
# credentials, so they must carry BASECAMP_NO_KEYRING=1 — per-command, not
# blanket: the `setup --help` capability probe stays bare (help
# short-circuits before the probe). e2e/run.sh exports BASECAMP_NO_KEYRING
# suite-wide, so the test shell must unset it first or the stub would see
# nk=1 even without the installer's belt.
@test "new binary: real setup calls carry BASECAMP_NO_KEYRING=1, capability probe does not" {
  write_nk_stub new
  run bash -c "
    set -euo pipefail
    unset BASECAMP_NO_KEYRING
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    post_install_setup basecamp
    cat '$LOG'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"nk=unset setup --help"* ]]
  [[ "$output" == *"nk=1 setup agents"* ]]
}

@test "old binary: skill install fallback carries BASECAMP_NO_KEYRING=1" {
  write_nk_stub old
  run bash -c "
    set -euo pipefail
    unset BASECAMP_NO_KEYRING
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    post_install_setup basecamp
    cat '$LOG'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"nk=unset setup --help"* ]]
  [[ "$output" == *"nk=1 skill install"* ]]
}

# The Windows canary can never prove the ps1 belt — Credential Manager works
# headlessly with or without it — so pin the behavior here. The function under
# test is extracted from install.ps1's AST and evaluated alone: Main never
# runs, and the installer needs no test hooks.
@test "install.ps1 Invoke-PostInstallSetup sets and restores BASECAMP_NO_KEYRING" {
  if ! command -v pwsh >/dev/null 2>&1; then
    if [[ -n "${CI:-}" ]]; then
      # Fail closed: a runner image dropping pwsh must surface as a failure,
      # not silent coverage loss.
      echo "pwsh is required in CI for install.ps1 belt coverage" >&2
      return 1
    fi
    skip "pwsh not installed"
  fi

  PS_LOG="$STUB_DIR/ps-calls.log"

  cat > "$STUB_DIR/basecamp-stub.ps1" <<'EOF'
$rest = $args -join ' '
$nk = if ($null -eq $env:BASECAMP_NO_KEYRING) { 'unset' } else { $env:BASECAMP_NO_KEYRING }
Add-Content -LiteralPath $env:PS_LOG "nk=$nk $rest"
if ($rest -eq 'setup --help') {
  '  agents  Install the Basecamp skill and connect detected coding agents'
}
EOF

  cat > "$STUB_DIR/driver.ps1" <<'EOF'
$ErrorActionPreference = 'Stop'
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:INSTALL_PS1_PATH, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw "install.ps1 parse errors: $($parseErrors -join '; ')" }
$fn = $ast.Find({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Invoke-PostInstallSetup' }, $true)
if (-not $fn) { throw 'Invoke-PostInstallSetup not found in install.ps1' }
# Evaluate only the function definition — the installer's Main never runs.
. ([scriptblock]::Create($fn.Extent.Text))
Invoke-PostInstallSetup $env:PS_STUB
if ($null -ne $env:BASECAMP_NO_KEYRING) { throw "BASECAMP_NO_KEYRING not restored: '$env:BASECAMP_NO_KEYRING'" }
'restored-ok'
EOF

  run bash -c "
    set -euo pipefail
    unset BASECAMP_NO_KEYRING
    export PS_LOG='$PS_LOG' PS_STUB='$STUB_DIR/basecamp-stub.ps1' INSTALL_PS1_PATH='$INSTALL_PS1'
    pwsh -NoProfile -File '$STUB_DIR/driver.ps1'
    cat '$PS_LOG'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"restored-ok"* ]]
  [[ "$output" == *"nk=1 setup agents"* ]]
}
