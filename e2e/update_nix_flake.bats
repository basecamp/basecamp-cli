#!/usr/bin/env bats
#
# Exit-status classification for scripts/update-nix-flake.sh.
#
# The PR-time flake build proves the *current* flake compiles; it says nothing
# about whether this script correctly tells a passing build from a failing one.
# That distinction is the actual defect that shipped: v0.8.0's flake could not
# build, and the script reported "vendorHash: verified (build succeeded)".
#
# `docker` is stubbed so these run anywhere, in milliseconds, and can reproduce
# failure modes that are otherwise awkward to stage.

setup() {
  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  WORK="$(mktemp -d)"
  STUB_DIR="$WORK/bin"
  mkdir -p "$STUB_DIR" "$WORK/repo/nix" "$WORK/repo/scripts"

  # The classifier travels with it — update-nix-flake.sh resolves it relative to
  # its own directory, and shares it with CI's nix-build-check.sh.
  cp "$REPO_ROOT/scripts/update-nix-flake.sh" \
     "$REPO_ROOT/scripts/extract-nix-vendor-hash.sh" "$WORK/repo/scripts/"
  cat > "$WORK/repo/nix/package.nix" <<'NIXPKG'
{
  version = "0.8.0";
  vendorHash = "sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDA=";
}
NIXPKG

  cd "$WORK/repo"
  git init -q .
  git config user.email t@t && git config user.name t
  git add -A && git commit -qm init
  # A stable tag at HEAD keeps go.mod/go.sum unchanged, so DEPS_CHANGED=false.
  git tag v0.8.0

  PATH="$STUB_DIR:$PATH"
}

teardown() {
  rm -rf "$WORK"
}

# Emits a canned nix log plus the sentinel the script reads for exit status.
stub_docker() {
  cat > "$STUB_DIR/docker" <<STUB
#!/usr/bin/env bash
cat <<'OUT'
$1
OUT
STUB
  chmod +x "$STUB_DIR/docker"
}

@test "succeeds when the nix build succeeds" {
  stub_docker "building '/nix/store/abc-basecamp-0.8.0-go-modules.drv'...
NIX_BUILD_EXIT=0"

  run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 2 ]   # 2 = no changes needed
  [[ "$output" == *"verified (build succeeded)"* ]]
}

@test "fails closed when the build fails without a hash mismatch" {
  # The exact v0.8.0 shape: nix logs that it is *building* basecamp, then dies
  # on the Go toolchain. The old heuristic matched that line and reported
  # success.
  stub_docker "building '/nix/store/abc-basecamp-0.8.0-go-modules.drv'...
       > go: go.mod requires go >= 1.26.5 (running go 1.26.4; GOTOOLCHAIN=local)
error: Build failed due to failed dependency
NIX_BUILD_EXIT=1"

  run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"not because of the vendorHash"* ]]
  [[ "$output" != *"verified (build succeeded)"* ]]
}

@test "records a corrected hash only after a rebuild proves it" {
  # First call reports the mismatch, second call succeeds.
  cat > "$STUB_DIR/docker" <<'STUB'
#!/usr/bin/env bash
STATE="${NIX_STUB_STATE:?}"
if [ -f "$STATE" ]; then
  echo "NIX_BUILD_EXIT=0"
else
  touch "$STATE"
  echo "error: hash mismatch in fixed-output derivation"
  echo "         specified: sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDA="
  echo "            got:    sha256-NEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWB="
  echo "NIX_BUILD_EXIT=1"
fi
STUB
  chmod +x "$STUB_DIR/docker"

  NIX_STUB_STATE="$WORK/called" run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 0 ]   # 0 = changes written
  [[ "$output" == *"vendorHash: updated"* ]]
  [[ "$output" == *"verified (build succeeded)"* ]]
  grep -q 'sha256-NEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWB=' nix/package.nix
}

@test "fails when the corrected hash still does not build" {
  # Every call reports a mismatch — the rebuild never converges, so the script
  # must not claim success just because it wrote a new hash.
  stub_docker "error: hash mismatch in fixed-output derivation
         specified: sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDA=
            got:    sha256-NEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWB=
NIX_BUILD_EXIT=1"

  run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"still fails after updating the vendorHash"* ]]
}

@test "does not mistake an unrelated 'got:' line for a hash mismatch" {
  # A failing build whose log happens to contain `got:` — a Go test assertion,
  # say — must take the non-hash failure path. Matching a bare `got:` would
  # capture "42" and write it into vendorHash, corrupting a tracked file while
  # reporting the wrong kind of failure.
  stub_docker "--- FAIL: TestSomething
    thing_test.go:42: got: 42
    thing_test.go:43: want: 7
error: builder failed with exit code 1
NIX_BUILD_EXIT=1"

  run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"not because of the vendorHash"* ]]
  [[ "$output" != *"vendorHash: updated"* ]]
  # The tracked file must be untouched.
  grep -q 'sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDA=' nix/package.nix
  ! grep -q '"42"' nix/package.nix
}

@test "verifies the build even when dependencies did not change" {
  # DEPS_CHANGED=false must not skip the build: the Go-toolchain drift that
  # broke v0.8.0 touches neither go.mod nor go.sum.
  stub_docker "NIX_BUILD_EXIT=0"

  run scripts/update-nix-flake.sh 0.8.0
  [ "$status" -eq 2 ]
  [[ "$output" == *"dependencies unchanged — verifying the Nix build anyway"* ]]
  [[ "$output" == *"verified (build succeeded)"* ]]
}
