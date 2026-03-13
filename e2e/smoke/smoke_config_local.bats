#!/usr/bin/env bats
# smoke_config_local.bats - Level 0: Local config and profile management

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

@test "profile create creates a profile" {
  run_smoke basecamp profile create "smoke-test-$(date +%s)" \
    --token "fake-token-for-test" --base-url "https://3.basecampapi.com" --json
  assert_success
}

@test "profile set-default sets the default profile" {
  # Create a profile first
  local name="smoke-default-$(date +%s)"
  basecamp profile create "$name" \
    --token "fake-token-for-test" --base-url "https://3.basecampapi.com" 2>/dev/null || {
    mark_unverifiable "Cannot create profile for set-default test"
    return
  }

  run_smoke basecamp profile set-default "$name"
  assert_success
}

@test "profile delete deletes a profile" {
  # Create a disposable profile
  local name="smoke-delete-$(date +%s)"
  basecamp profile create "$name" \
    --token "fake-token-for-test" --base-url "https://3.basecampapi.com" 2>/dev/null || {
    mark_unverifiable "Cannot create profile for delete test"
    return
  }

  run_smoke basecamp profile delete "$name" --force
  assert_success
}
