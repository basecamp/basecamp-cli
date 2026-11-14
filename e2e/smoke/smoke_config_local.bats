#!/usr/bin/env bats
# smoke_config_local.bats - Level 0: Local config operations

load smoke_helper

# No setup_file needed — these operate on local config only

@test "config trust trusts a config file" {
  local dir="$BATS_TEST_TMPDIR/smoke-trust-test/.basecamp"
  mkdir -p "$dir"
  printf '%s\n' '{}' > "$dir/config.json"

  run_smoke basecamp config trust "$dir/config.json"
  assert_success
}

@test "config untrust untrusts a config file" {
  local dir="$BATS_TEST_TMPDIR/smoke-trust-test/.basecamp"
  mkdir -p "$dir"
  printf '%s\n' '{}' > "$dir/config.json"

  # Trust first so untrust has something to remove.
  run_smoke basecamp config trust "$dir/config.json"
  assert_success

  run_smoke basecamp config untrust "$dir/config.json"
  assert_success
}
