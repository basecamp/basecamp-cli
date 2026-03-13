#!/usr/bin/env bats
# smoke_config_local.bats - Level 0: Local config operations

load smoke_helper

# No setup_file needed — these operate on local config only

@test "config trust trusts a directory" {
  local dir="$BATS_FILE_TMPDIR/smoke-trust-test"
  mkdir -p "$dir"

  run_smoke basecamp config trust "$dir"
  assert_success
}

@test "config untrust untrusts a directory" {
  local dir="$BATS_FILE_TMPDIR/smoke-trust-test"
  mkdir -p "$dir"

  # Trust first so untrust has something to remove
  basecamp config trust "$dir" 2>/dev/null || true

  run_smoke basecamp config untrust "$dir"
  assert_success
}
