#!/usr/bin/env bats
# auth.bats - Tests for lib/auth.sh

load test_helper


# Auth status

@test "bcq auth status shows unauthenticated when no credentials" {
  unset BASECAMP_TOKEN

  run bcq --md auth status
  assert_success
  assert_output_contains "Not authenticated"
}

@test "bcq auth status --json returns JSON" {
  unset BASECAMP_TOKEN

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
  export BASECAMP_TOKEN="test-token"

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
  # macOS uses -f for format, Linux uses -c
  if stat -f "%Lp" / >/dev/null 2>&1; then
    perms=$(stat -f "%Lp" "$TEST_HOME/.config/basecamp/credentials.json")
  else
    perms=$(stat -c "%a" "$TEST_HOME/.config/basecamp/credentials.json")
  fi

  [[ "$perms" == "600" ]]
}


# Scope handling

@test "bcq auth status shows read-only for read scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "read"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "read-only"
}

@test "bcq auth status shows read+write for full scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "full"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "read+write"
}

@test "bcq auth status JSON includes scope" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "read"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

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


# OAuth provider handling

@test "bcq auth status shows Basecamp OAuth 2.1 provider" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "full" "bc3"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "Basecamp OAuth 2.1"
}

@test "bcq auth status shows Launchpad OAuth 2 provider" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "full" "launchpad"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

  run bcq --md auth status
  assert_success
  assert_output_contains "Launchpad OAuth 2"
}

@test "bcq auth status JSON includes oauth_provider" {
  create_credentials "test-token" "$(($(date +%s) + 3600))" "full" "launchpad"
  create_accounts
  create_global_config '{"account_id": "99999"}'

  export BASECAMP_TOKEN="test-token"

  run bcq auth status --json
  assert_success
  is_valid_json
  assert_json_value ".oauth_provider" "launchpad"
}

@test "_load_launchpad_client loads from environment variables" {
  export BCQ_CLIENT_ID="test-client-id"
  export BCQ_CLIENT_SECRET="test-client-secret"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  _load_launchpad_client
  [[ "$client_id" == "test-client-id" ]]
  [[ "$client_secret" == "test-client-secret" ]]
}

@test "_load_launchpad_client loads from config" {
  unset BCQ_CLIENT_ID BCQ_CLIENT_SECRET
  create_global_config '{"oauth_client_id": "config-client-id", "oauth_client_secret": "config-client-secret"}'

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  _load_launchpad_client
  [[ "$client_id" == "config-client-id" ]]
  [[ "$client_secret" == "config-client-secret" ]]
}

@test "_load_launchpad_client fails when no credentials for custom hosts" {
  unset BCQ_CLIENT_ID BCQ_CLIENT_SECRET
  create_global_config '{}'
  export BCQ_LAUNCHPAD_URL="http://launchpad.localhost:3011"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  run _load_launchpad_client
  assert_failure
}

@test "_load_launchpad_client uses built-in defaults for production" {
  unset BCQ_CLIENT_ID BCQ_CLIENT_SECRET
  create_global_config '{}'
  export BCQ_LAUNCHPAD_URL="https://launchpad.37signals.com"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  _load_launchpad_client
  [[ "$client_id" == "5fdd0da8e485ae6f80f4ce0a4938640bb22f1348" ]]
  [[ "$client_secret" == "a3dc33d78258e828efd6768ac2cd67f32ec1910a" ]]
}

@test "_load_launchpad_client uses built-in defaults with trailing slash" {
  unset BCQ_CLIENT_ID BCQ_CLIENT_SECRET
  create_global_config '{}'
  export BCQ_LAUNCHPAD_URL="https://launchpad.37signals.com/"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  _load_launchpad_client
  [[ "$client_id" == "5fdd0da8e485ae6f80f4ce0a4938640bb22f1348" ]]
  [[ "$client_secret" == "a3dc33d78258e828efd6768ac2cd67f32ec1910a" ]]
}

@test "_get_oauth_type defaults to launchpad when discovery fails" {
  # Use a fake base URL that doesn't exist
  # Also set local Launchpad URL to avoid localhost protection check
  export BCQ_BASE_URL="http://127.0.0.1:19999"
  export BCQ_LAUNCHPAD_URL="http://launchpad.localhost:3011"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  local oauth_type
  oauth_type=$(_get_oauth_type)

  [[ "$oauth_type" == "launchpad" ]]
}

@test "localhost with production Launchpad fails with helpful error" {
  # When using localhost base URL with production Launchpad, should error
  export BCQ_BASE_URL="http://127.0.0.1:19999"
  export BCQ_LAUNCHPAD_URL="https://launchpad.37signals.com"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  run _get_oauth_type

  [[ "$status" -ne 0 ]]
  [[ "$output" == *"Local dev OAuth requires configuration"* ]]
  [[ "$output" == *"BCQ_LAUNCHPAD_URL"* ]]
}


# Token refresh tests

# Helper to create a mock curl that captures the URL and params
setup_refresh_mock_curl() {
  mkdir -p "$BATS_TEST_TMPDIR/bin"

  # Use double-quoted heredoc to expand BATS_TEST_TMPDIR now
  cat > "$BATS_TEST_TMPDIR/bin/curl" <<MOCK_CURL
#!/bin/bash
# Capture the URL (last argument that looks like a URL)
for arg in "\$@"; do
  if [[ "\$arg" == http* ]]; then
    echo "\$arg" >> "$BATS_TEST_TMPDIR/curl_urls"
  fi
done

# Capture --data-urlencode params
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "--data-urlencode" ]]; then
    echo "\$arg" >> "$BATS_TEST_TMPDIR/curl_params"
  fi
  prev="\$arg"
done

# Return a successful refresh response
echo '{"access_token": "new-token", "refresh_token": "new-refresh", "expires_in": 7200}'
MOCK_CURL
  chmod +x "$BATS_TEST_TMPDIR/bin/curl"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "refresh_token uses stored token_endpoint" {
  # Create credentials with stored token_endpoint (new behavior)
  create_credentials "test-token" "$(($(date +%s) - 100))" "full" "launchpad" "https://stored.endpoint/token"
  export BCQ_CLIENT_ID="test-client-id"
  export BCQ_CLIENT_SECRET="test-client-secret"
  # Set a different Launchpad URL to verify stored endpoint takes precedence
  export BCQ_LAUNCHPAD_TOKEN_URL="https://launchpad.test/authorization/token"

  setup_refresh_mock_curl

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  refresh_token

  # Verify the stored token endpoint was used, not the env var
  local url
  url=$(cat "$BATS_TEST_TMPDIR/curl_urls")
  [[ "$url" == "https://stored.endpoint/token" ]]
}

@test "refresh_token falls back to BCQ_LAUNCHPAD_TOKEN_URL for legacy credentials" {
  # Create credentials WITHOUT stored token_endpoint (legacy behavior)
  create_credentials "test-token" "$(($(date +%s) - 100))" "full" "launchpad"
  export BCQ_CLIENT_ID="test-client-id"
  export BCQ_CLIENT_SECRET="test-client-secret"
  export BCQ_LAUNCHPAD_TOKEN_URL="https://launchpad.test/authorization/token"

  setup_refresh_mock_curl

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  refresh_token

  # Verify the fallback Launchpad token URL was used
  local url
  url=$(cat "$BATS_TEST_TMPDIR/curl_urls")
  [[ "$url" == "https://launchpad.test/authorization/token" ]]
}

@test "refresh_token uses type=refresh param for launchpad" {
  create_credentials "test-token" "$(($(date +%s) - 100))" "full" "launchpad"
  export BCQ_CLIENT_ID="test-client-id"
  export BCQ_CLIENT_SECRET="test-client-secret"
  export BCQ_LAUNCHPAD_TOKEN_URL="https://launchpad.test/authorization/token"

  setup_refresh_mock_curl

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  refresh_token

  # Verify Launchpad-specific param (type=refresh instead of grant_type=refresh_token)
  local params
  params=$(cat "$BATS_TEST_TMPDIR/curl_params")
  [[ "$params" == *"type=refresh"* ]]
}

@test "refresh_token uses grant_type=refresh_token for bc3" {
  # Set base URL before creating credentials so they match
  export BCQ_BASE_URL="http://127.0.0.1:19998"

  create_credentials "test-token" "$(($(date +%s) - 100))" "full" "bc3"

  # Create BC3 client file
  mkdir -p "$TEST_HOME/.config/basecamp"
  echo '{"client_id": "bc3-client-id", "client_secret": "bc3-client-secret"}' > "$TEST_HOME/.config/basecamp/client.json"

  # Mock BC3 discovery to return a token endpoint
  mkdir -p "$BATS_TEST_TMPDIR/mock_discovery"
  cat > "$BATS_TEST_TMPDIR/mock_discovery/curl" <<DISCOVERY_MOCK
#!/bin/bash
# If this is a discovery request, return mock config
if [[ "\$*" == *".well-known"* ]]; then
  echo '{"authorization_endpoint": "http://test/auth", "token_endpoint": "http://bc3.test/oauth/token"}'
  exit 0
fi
# Otherwise it's a refresh request - capture URL and params
for arg in "\$@"; do
  if [[ "\$arg" == http* ]]; then
    echo "\$arg" >> "$BATS_TEST_TMPDIR/curl_urls"
  fi
done
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "--data-urlencode" ]]; then
    echo "\$arg" >> "$BATS_TEST_TMPDIR/curl_params"
  fi
  prev="\$arg"
done
echo '{"access_token": "new-token", "refresh_token": "new-refresh", "expires_in": 7200}'
DISCOVERY_MOCK
  chmod +x "$BATS_TEST_TMPDIR/mock_discovery/curl"
  export PATH="$BATS_TEST_TMPDIR/mock_discovery:$PATH"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  refresh_token

  # Verify BC3-specific param (grant_type=refresh_token)
  local params
  params=$(cat "$BATS_TEST_TMPDIR/curl_params")
  [[ "$params" == *"grant_type=refresh_token"* ]]
}

@test "refresh_token preserves oauth_type in credentials" {
  create_credentials "test-token" "$(($(date +%s) - 100))" "full" "launchpad"
  export BCQ_CLIENT_ID="test-client-id"
  export BCQ_CLIENT_SECRET="test-client-secret"
  export BCQ_LAUNCHPAD_TOKEN_URL="https://launchpad.test/authorization/token"

  setup_refresh_mock_curl

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"
  source "$BCQ_ROOT/lib/auth.sh"

  refresh_token

  # Verify oauth_type is preserved in saved credentials
  local saved_type
  saved_type=$(load_credentials | jq -r '.oauth_type')
  [[ "$saved_type" == "launchpad" ]]
}
