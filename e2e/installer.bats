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

@test "install.sh gives piped first-time setup the controlling terminal" {
  run grep -F '"$BIN_DIR/$binary_name" setup </dev/tty' "$INSTALL_SH"
  [[ "$status" -eq 0 ]]
  run grep -F '[[ -t 1 ]] && [[ -t 2 ]] && [[ -c /dev/tty ]]' "$INSTALL_SH"
  [[ "$status" -eq 0 ]]
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
# Second pass: a caller-set value must survive, not just absence.
$env:BASECAMP_NO_KEYRING = '0'
Invoke-PostInstallSetup $env:PS_STUB
if ($env:BASECAMP_NO_KEYRING -ne '0') { throw "existing BASECAMP_NO_KEYRING value not restored: '$env:BASECAMP_NO_KEYRING'" }
'restored-value-ok'
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
  [[ "$output" == *"restored-value-ok"* ]]
  [[ "$output" == *"nk=1 setup agents"* ]]
}

# Smart App Control blocks unsigned executables at process creation (releases
# up to v0.8.0-rc.1 ship an unsigned basecamp.exe), so the install scripts
# must surface WHY the first run failed instead of a bare "not working".

@test "verify_install surfaces stderr and adds the Windows SAC hint" {
  cat > "$STUB_DIR/basecamp.exe" <<'EOF'
#!/usr/bin/env bash
echo "simulated block: cannot execute" >&2
exit 126
EOF
  chmod +x "$STUB_DIR/basecamp.exe"

  run bash -c "
    set -euo pipefail
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    verify_install windows_amd64
  "
  [[ "$status" -ne 0 ]]
  [[ "$output" == *"simulated block: cannot execute"* ]]
  [[ "$output" == *"Smart App Control"* ]]
  [[ "$output" == *"#windows-smart-app-control-and-smartscreen"* ]]
}

@test "verify_install on linux surfaces stderr without the Windows hint" {
  cat > "$STUB_DIR/basecamp" <<'EOF'
#!/usr/bin/env bash
echo "simulated failure" >&2
exit 1
EOF
  chmod +x "$STUB_DIR/basecamp"

  run bash -c "
    set -euo pipefail
    source '$INSTALL_SH'
    BIN_DIR='$STUB_DIR'
    verify_install linux_amd64
  "
  [[ "$status" -ne 0 ]]
  [[ "$output" == *"simulated failure"* ]]
  [[ "$output" != *"Smart App Control"* ]]
}

@test "install.ps1 carries the Smart App Control first-run diagnosis" {
  grep -q 'function Get-FirstRunFailureMessage' "$INSTALL_PS1"
  grep -q 'Smart App Control' "$INSTALL_PS1"
  grep -q 'Protection history' "$INSTALL_PS1"
  # The first-run failure path routes through the diagnosis helper.
  grep -qF 'Fail (Get-FirstRunFailureMessage' "$INSTALL_PS1"
}

# All three diagnosis branches, driven by shadowing the probes. Same AST
# extraction pattern as the BASECAMP_NO_KEYRING test above: only the function
# under test is evaluated, Main never runs.
@test "install.ps1 Get-FirstRunFailureMessage diagnoses SAC, quarantine, and generic failures" {
  if ! command -v pwsh >/dev/null 2>&1; then
    if [[ -n "${CI:-}" ]]; then
      echo "pwsh is required in CI for install.ps1 diagnosis coverage" >&2
      return 1
    fi
    skip "pwsh not installed"
  fi

  cat > "$STUB_DIR/sac-driver.ps1" <<'EOF'
$ErrorActionPreference = 'Stop'
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:INSTALL_PS1_PATH, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw "install.ps1 parse errors: $($parseErrors -join '; ')" }
$fn = $ast.Find({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Get-FirstRunFailureMessage' }, $true)
if (-not $fn) { throw 'Get-FirstRunFailureMessage not found in install.ps1' }
. ([scriptblock]::Create($fn.Extent.Text))

# Shadow the probes the helper relies on. Advanced functions so the helper's
# -ErrorAction Stop common parameter binds.
$script:SigStatus = 'NotSigned'
$script:SacState = 1
function Get-AuthenticodeSignature { [CmdletBinding()] param([string]$FilePath) [pscustomobject]@{ Status = $script:SigStatus } }
function Get-ItemProperty { [CmdletBinding()] param([string]$Path) [pscustomobject]@{ VerifiedAndReputablePolicyState = $script:SacState } }

# Branch 1: unsigned binary, SAC on.
$msg = Get-FirstRunFailureMessage -Binary 'C:\bin\basecamp.exe' -Reason 'boom-sac'
if ($msg -notmatch '^Installed basecamp\.exe .+ running it failed: boom-sac') { throw "branch1 does not lead with the original failure: $msg" }
if ($msg -notlike '*Smart App Control*') { throw 'branch1 missing SAC explanation' }
if ($msg -notlike '*wsl --install*') { throw 'branch1 missing WSL option' }
if ($msg -notlike '*no per-app exceptions*') { throw 'branch1 missing no-exceptions caveat' }
if ($msg -notlike '*leave it off while using this unsigned build*') { throw 'branch1 missing stay-off caveat' }
'branch1-ok'

# Branch 2: unsigned binary, SAC off — quarantine advice, no WSL pitch.
$script:SacState = 0
$msg = Get-FirstRunFailureMessage -Binary 'C:\bin\basecamp.exe' -Reason 'boom-quarantine'
if ($msg -notlike '*boom-quarantine*') { throw 'branch2 missing original failure' }
if ($msg -notlike '*Protection history*') { throw 'branch2 missing Protection history advice' }
if ($msg -like '*wsl --install*') { throw 'branch2 should not pitch WSL' }
'branch2-ok'

# Branch 3: signed binary — generic hint, no unsigned claim.
$script:SigStatus = 'Valid'
$msg = Get-FirstRunFailureMessage -Binary 'C:\bin\basecamp.exe' -Reason 'boom-generic'
if ($msg -notlike '*boom-generic*') { throw 'branch3 missing original failure' }
if ($msg -notlike '*Protection history*') { throw 'branch3 missing generic hint' }
if ($msg -like '*not code-signed*') { throw 'branch3 must not claim the binary is unsigned' }
'branch3-ok'
EOF

  run bash -c "
    set -euo pipefail
    export INSTALL_PS1_PATH='$INSTALL_PS1'
    pwsh -NoProfile -File '$STUB_DIR/sac-driver.ps1'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"branch1-ok"* ]]
  [[ "$output" == *"branch2-ok"* ]]
  [[ "$output" == *"branch3-ok"* ]]
}

# Releases publish the new (protobuf) Sigstore bundle format. cosign v3 parses
# it by default, v2.6–v2.x needs --new-bundle-format=true, and older cosign
# cannot verify it at all (v2.4 chokes on the bundle's tlog key type, v2.2
# lacks the flag) — those must warn and skip, preserving cosign's optional
# posture, never fail the install or false-green the verification.

@test "cosign_bundle_support: v3 verifies with no extra flag" {
  run bash -c "source '$INSTALL_SH'; cosign_bundle_support v3.0.2"
  [[ "$status" -eq 0 ]]
  [[ -z "$output" ]]
}

@test "cosign_bundle_support: v2.6 verifies with --new-bundle-format=true" {
  run bash -c "source '$INSTALL_SH'; cosign_bundle_support v2.6.0"
  [[ "$status" -eq 0 ]]
  [[ "$output" == "--new-bundle-format=true" ]]
}

@test "cosign_bundle_support: v2.4 is unsupported" {
  run bash -c "source '$INSTALL_SH'; cosign_bundle_support v2.4.0"
  [[ "$status" -ne 0 ]]
}

@test "cosign_bundle_support: garbage version is unsupported" {
  run bash -c "source '$INSTALL_SH'; cosign_bundle_support 'devel-deadbeef'"
  [[ "$status" -ne 0 ]]
  run bash -c "source '$INSTALL_SH'; cosign_bundle_support ''"
  [[ "$status" -ne 0 ]]
}

# write_cosign_stub emits a cosign stub whose `cosign version` reports the
# given GitVersion and which logs every other invocation's argv.
write_cosign_stub() {
  local ver="$1"
  {
    echo '#!/usr/bin/env bash'
    echo 'if [[ "$1" == "version" ]]; then'
    echo "  echo 'GitVersion:    ${ver}'"
    echo '  exit 0'
    echo 'fi'
    echo "echo \"\$@\" >> \"$LOG\""
    echo 'exit 0'
  } > "$STUB_DIR/cosign"
  chmod +x "$STUB_DIR/cosign"
}

# run_verify_checksums drives the real verify_checksums with a stubbed cosign
# and a no-network curl_run, against a locally-built archive + checksums.txt.
run_verify_checksums() {
  run bash -c "
    set -euo pipefail
    export PATH=\"$STUB_DIR:\$PATH\"
    source '$INSTALL_SH'
    tmp_dir='$STUB_DIR/work'
    mkdir -p \"\$tmp_dir\"
    printf 'archive-bytes' > \"\$tmp_dir/basecamp_9.9.9_linux_amd64.tar.gz\"
    sha=\$(cd \"\$tmp_dir\" && \$(find_sha256_cmd) basecamp_9.9.9_linux_amd64.tar.gz | awk '{print \$1}')
    verify_checksums_stub_sums() { printf '%s  basecamp_9.9.9_linux_amd64.tar.gz\n' \"\$sha\" > \"\$tmp_dir/checksums.txt\"; }
    # No-network curl_run: serve the locally-computed checksums.txt and a
    # placeholder bundle (cosign is stubbed, so its content is irrelevant).
    curl_run() {
      local arg dest=''
      local prev=''
      for arg in \"\$@\"; do
        [[ \"\$prev\" == '-o' ]] && dest=\"\$arg\"
        prev=\"\$arg\"
      done
      if [[ \"\$dest\" == *checksums.txt ]]; then
        verify_checksums_stub_sums
      elif [[ -n \"\$dest\" ]]; then
        printf 'stub-bundle' > \"\$dest\"
      fi
      return 0
    }
    verify_checksums 9.9.9 \"\$tmp_dir\" basecamp_9.9.9_linux_amd64.tar.gz
    cat '$LOG' 2>/dev/null || true
  "
}

@test "verify_checksums with cosign v3 runs verify-blob without the compat flag" {
  write_cosign_stub v3.0.2
  run_verify_checksums
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"Signature verified"* ]]
  [[ "$output" == *"verify-blob"* ]]
  [[ "$output" != *"--new-bundle-format"* ]]
}

@test "verify_checksums with cosign v2.6 adds --new-bundle-format=true" {
  write_cosign_stub v2.6.0
  run_verify_checksums
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"Signature verified"* ]]
  [[ "$output" == *"--new-bundle-format=true"* ]]
}

@test "verify_checksums with cosign v2.4 warns, skips verification, still succeeds" {
  write_cosign_stub v2.4.0
  run_verify_checksums
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"Checksum verified"* ]]
  [[ "$output" == *"Skipping signature verification"* ]]
  [[ "$output" != *"verify-blob"* ]]
}

@test "verify_checksums with unparseable cosign version warns and skips" {
  write_cosign_stub 'devel'
  run_verify_checksums
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"Skipping signature verification"* ]]
  [[ "$output" != *"verify-blob"* ]]
}

# A cosign whose `version` subcommand itself FAILS must not abort the
# installer: under `set -euo pipefail` the version probe runs inside a command
# substitution, and without the guard in cosign_version the nonzero exit
# propagates and kills the install (reproduced as exit 42). It must degrade to
# the warn-and-skip path with checksum verification intact.
@test "verify_checksums with broken cosign (version exits nonzero) warns, skips, still succeeds" {
  {
    echo '#!/usr/bin/env bash'
    echo 'if [[ "$1" == "version" ]]; then'
    echo '  echo "cosign: catastrophic startup failure" >&2'
    echo '  exit 42'
    echo 'fi'
    echo "echo \"\$@\" >> \"$LOG\""
    echo 'exit 0'
  } > "$STUB_DIR/cosign"
  chmod +x "$STUB_DIR/cosign"

  run_verify_checksums
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"Checksum verified"* ]]
  [[ "$output" == *"Skipping signature verification"* ]]
  [[ "$output" != *"verify-blob"* ]]
}

# install.ps1 equivalent, executed (not review-only): extract the two cosign
# functions from the AST — Main never runs — shadow cosign/Download-File, and
# drive all three version tiers.
@test "install.ps1 cosign version gate selects bare, flagged, and skip paths" {
  if ! command -v pwsh >/dev/null 2>&1; then
    if [[ -n "${CI:-}" ]]; then
      echo "pwsh is required in CI for install.ps1 cosign gate coverage" >&2
      return 1
    fi
    skip "pwsh not installed"
  fi

  cat > "$STUB_DIR/cosign-driver.ps1" <<'EOF'
$ErrorActionPreference = 'Stop'
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($env:INSTALL_PS1_PATH, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw "install.ps1 parse errors: $($parseErrors -join '; ')" }
foreach ($name in @('Get-CosignBundleSupport', 'Verify-CosignSignature')) {
  $fn = $ast.Find({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name }, $true)
  if (-not $fn) { throw "$name not found in install.ps1" }
  . ([scriptblock]::Create($fn.Extent.Text))
}

function Step([string]$Message) { Write-Host "step: $Message" }
function Info([string]$Message) { Write-Host "info: $Message" }
function Fail([string]$Message) { throw $Message }
function Download-File([string]$Url, [string]$Destination) { Set-Content -Path $Destination -Value 'stub-bundle' }

$script:CosignVersion = ''
$script:CosignArgs = $null
function cosign {
  if ($args[0] -eq 'version') {
    "GitVersion:    $script:CosignVersion"
  } else {
    $script:CosignArgs = $args -join ' '
  }
  $global:LASTEXITCODE = 0
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
Set-Content -Path (Join-Path $tmp 'checksums.txt') -Value 'stub'

# v3: bare verify-blob, no compat flag.
$script:CosignVersion = 'v3.0.2'; $script:CosignArgs = $null
Verify-CosignSignature -Version '9.9.9' -BaseUrl 'https://example.invalid' -TmpDir $tmp
if (-not $script:CosignArgs) { throw 'v3: cosign verify-blob was not invoked' }
if ($script:CosignArgs -notmatch 'verify-blob') { throw "v3: unexpected args: $script:CosignArgs" }
if ($script:CosignArgs -match 'new-bundle-format') { throw "v3: compat flag must not be passed: $script:CosignArgs" }
'v3-ok'

# v2.6: compat flag required.
$script:CosignVersion = 'v2.6.0'; $script:CosignArgs = $null
Verify-CosignSignature -Version '9.9.9' -BaseUrl 'https://example.invalid' -TmpDir $tmp
if ($script:CosignArgs -notmatch '--new-bundle-format=true') { throw "v2.6: compat flag missing: $script:CosignArgs" }
'v26-ok'

# v2.4: warn + skip, verify-blob never runs.
$script:CosignVersion = 'v2.4.0'; $script:CosignArgs = $null
Verify-CosignSignature -Version '9.9.9' -BaseUrl 'https://example.invalid' -TmpDir $tmp
if ($null -ne $script:CosignArgs) { throw "v2.4: verify-blob must not run: $script:CosignArgs" }
'v24-ok'

# Garbage version: warn + skip.
$script:CosignVersion = 'devel'; $script:CosignArgs = $null
Verify-CosignSignature -Version '9.9.9' -BaseUrl 'https://example.invalid' -TmpDir $tmp
if ($null -ne $script:CosignArgs) { throw "garbage: verify-blob must not run: $script:CosignArgs" }
'garbage-ok'

Remove-Item -Recurse -Force $tmp
EOF

  run bash -c "
    set -euo pipefail
    export INSTALL_PS1_PATH='$INSTALL_PS1'
    pwsh -NoProfile -File '$STUB_DIR/cosign-driver.ps1'
  "
  [[ "$status" -eq 0 ]]
  [[ "$output" == *"v3-ok"* ]]
  [[ "$output" == *"v26-ok"* ]]
  [[ "$output" == *"v24-ok"* ]]
  [[ "$output" == *"garbage-ok"* ]]
}
