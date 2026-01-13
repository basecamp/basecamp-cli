#!/usr/bin/env bats
# auth.bats - Tests for lib/auth.sh

load test_helper


# Auth status

@test "bcq auth status shows unauthenticated when no credentials" {
  unset BASECAMP_ACCESS_TOKEN

  run bcq --md auth status
  assert_success
  assert_output_contains "Not authenticated"
}

@test "bcq auth status --json returns JSON" {
  unset BASECAMP_ACCESS_TOKEN

  run bcq auth status --json
  assert_success
  is_valid_json
  assert_json_value ".status" "unauthenticated"
}

@test "bcq auth status shows authenticated with valid credentials" {
  create_credentials "test-token" "$(($(date +%s) + 3600))"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  # Use env token since we can't easily test file-based auth in this context
  export BASECAMP_ACCESS_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "Authenticated"
}


# Auth logout

@test "bcq auth logout removes credentials" {
  create_credentials "test-token"

  run bcq auth logout
  assert_success

  # With multi-origin support, file exists but entry for current base URL is removed
  local base_url="${BCQ_BASE_URL:-https://3.basecampapi.com}"
  base_url="${base_url%/}"
  local creds
  creds=$(jq -r --arg url "$base_url" '.[$url] // empty' "$TEST_HOME/.config/basecamp/credentials.json")
  [[ -z "$creds" ]]
}

@test "bcq auth logout handles missing credentials gracefully" {
  run bcq auth logout
  assert_success
  assert_output_contains "Not logged in"
}


# Credential file permissions

@test "credentials file has restricted permissions when saved" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  save_credentials '{"access_token": "test", "refresh_token": "test", "expires_at": 9999999999}'

  local perms
  perms=$(stat -f "%Lp" "$TEST_HOME/.config/basecamp/credentials.json" 2>/dev/null || stat -c "%a" "$TEST_HOME/.config/basecamp/credentials.json" 2>/dev/null)

  [[ "$perms" == "600" ]]
}


# Scope handling

@test "bcq auth status shows read-only for read scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "read"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_ACCESS_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "read-only"
}

@test "bcq auth status shows read+write for full scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "full"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_ACCESS_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "read+write"
}

@test "bcq auth status JSON includes scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "read"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_ACCESS_TOKEN="test-token"

  run bcq auth status --json
  assert_success
  is_valid_json
  assert_json_value ".scope" "read"
}

@test "get_token_scope returns scope from credentials" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "read"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  local scope
  scope=$(get_token_scope)

  [[ "$scope" == "read" ]]
}

@test "get_token_scope returns unknown when no scope stored" {
  create_credentials "test-token" "$(($(date +%s) + 3600))"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  run get_token_scope
  assert_failure
  assert_output_contains "unknown"
}
