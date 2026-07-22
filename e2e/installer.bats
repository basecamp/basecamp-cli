#!/usr/bin/env bats
# installer.bats - Tests for the install scripts' post-install agent setup.
#
# #528: the non-TTY and skip branches must run `setup agents` (baseline skill +
# best-effort agent connection), never the hardcoded `setup claude`.

setup() {
  # The installer contract keys off these; a leaked value would skew results.
  unset BASECAMP_SKIP_SETUP BASECAMP_SETUP_AGENT

  INSTALL_SH="${BATS_TEST_DIRNAME}/../scripts/install.sh"
  INSTALL_PS1="${BATS_TEST_DIRNAME}/../scripts/install.ps1"

  STUB_DIR="$(mktemp -d)"
  LOG="$STUB_DIR/calls.log"

  # Stub `basecamp` that logs its argv so we can see which subcommand ran.
  cat > "$STUB_DIR/basecamp" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$LOG"
EOF
  chmod +x "$STUB_DIR/basecamp"
}

teardown() {
  [[ -n "${STUB_DIR:-}" ]] && rm -rf "$STUB_DIR"
}

# The if-form guard must let sourcing define functions without running main.
@test "install.sh can be sourced without running the installer" {
  run bash -c "set -euo pipefail; source '$INSTALL_SH'; echo sourced-ok"
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"sourced-ok"* ]]
  [[ "$output" != *"Basecamp CLI"* ]]  # banner would print if main ran
}

@test "post_install_setup dispatches to 'setup agents', never 'setup claude'" {
  run bash -c "
    set -euo pipefail
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    post_install_setup basecamp
    cat '$LOG'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup agents"* ]]
  [[ "$output" != *"setup claude"* ]]
}

@test "post_install_setup honors BASECAMP_SKIP_SETUP path (still 'setup agents')" {
  run bash -c "
    set -euo pipefail
    export BASECAMP_SKIP_SETUP=1
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    post_install_setup basecamp
    cat '$LOG'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"setup agents"* ]]
  [[ "$output" != *"setup claude"* ]]
}

@test "install.sh has no residual 'setup claude'" {
  run grep -n "setup claude" "$INSTALL_SH"
  [[ "$status" -ne 0 ]]  # grep exits non-zero when nothing matches
}

@test "install.sh skip and non-tty branches both dispatch via post_install_setup" {
  run grep -c 'post_install_setup "\$binary_name"' "$INSTALL_SH"
  [[ "$status" -eq 0 ]]
  [[ "$output" -ge 2 ]]
}

@test "install.ps1 skip and non-interactive branches each invoke 'setup agents'" {
  run grep -c '& \$installedBinary setup agents' "$INSTALL_PS1"
  [[ "$status" -eq 0 ]]
  [[ "$output" -eq 2 ]]
}
