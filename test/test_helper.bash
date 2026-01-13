#!/usr/bin/env bash
# test_helper.bash - Common test utilities for bcq tests


# Setup/Teardown

setup() {
  # Store original environment
  _ORIG_HOME="$HOME"
  _ORIG_PWD="$PWD"

  # Create temp directories
  TEST_TEMP_DIR="$(mktemp -d)"
  TEST_HOME="$TEST_TEMP_DIR/home"
  TEST_PROJECT="$TEST_TEMP_DIR/project"

  mkdir -p "$TEST_HOME/.config/basecamp"
  mkdir -p "$TEST_PROJECT/.basecamp"

  # Set up test environment
  export HOME="$TEST_HOME"
  export BCQ_ROOT="${BATS_TEST_DIRNAME}/.."
  export PATH="$BCQ_ROOT/bin:$PATH"

  # Clear environment variables that might interfere with tests
  # Tests can set these as needed
  unset BASECAMP_ACCESS_TOKEN
  unset BASECAMP_ACCOUNT_ID
  unset BASECAMP_PROJECT_ID
  unset BCQ_ACCOUNT
  unset BCQ_PROJECT

  cd "$TEST_PROJECT"
}

teardown() {
  # Restore original environment
  export HOME="$_ORIG_HOME"
  cd "$_ORIG_PWD"

  # Clean up temp directory
  if [[ -d "$TEST_TEMP_DIR" ]]; then
    rm -rf "$TEST_TEMP_DIR"
  fi
}


# Assertions

assert_success() {
  if [[ "$status" -ne 0 ]]; then
    echo "Expected success (0), got $status"
    echo "Output: $output"
    return 1
  fi
}

assert_failure() {
  if [[ "$status" -eq 0 ]]; then
    echo "Expected failure (non-zero), got $status"
    echo "Output: $output"
    return 1
  fi
}

assert_exit_code() {
  local expected="$1"
  if [[ "$status" -ne "$expected" ]]; then
    echo "Expected exit code $expected, got $status"
    echo "Output: $output"
    return 1
  fi
}

assert_output_contains() {
  local expected="$1"
  if [[ "$output" != *"$expected"* ]]; then
    echo "Expected output to contain: $expected"
    echo "Actual output: $output"
    return 1
  fi
}

assert_output_not_contains() {
  local unexpected="$1"
  if [[ "$output" == *"$unexpected"* ]]; then
    echo "Expected output NOT to contain: $unexpected"
    echo "Actual output: $output"
    return 1
  fi
}

assert_output_starts_with() {
  local expected="$1"
  if [[ "${output:0:${#expected}}" != "$expected" ]]; then
    echo "Expected output to start with: $expected"
    echo "Actual output starts with: ${output:0:20}"
    return 1
  fi
}

assert_json_value() {
  local path="$1"
  local expected="$2"
  local actual
  actual=$(echo "$output" | jq -r "$path")

  if [[ "$actual" != "$expected" ]]; then
    echo "JSON path $path: expected '$expected', got '$actual'"
    echo "Full output: $output"
    return 1
  fi
}

assert_json_not_null() {
  local path="$1"
  local actual
  actual=$(echo "$output" | jq -r "$path")

  if [[ "$actual" == "null" ]] || [[ -z "$actual" ]]; then
    echo "JSON path $path: expected non-null value, got '$actual'"
    return 1
  fi
}


# Fixtures

create_global_config() {
  local content="${1:-{}}"
  echo "$content" > "$TEST_HOME/.config/basecamp/config.json"
}

create_local_config() {
  local content="${1:-{}}"
  echo "$content" > "$TEST_PROJECT/.basecamp/config.json"
}

create_credentials() {
  local access_token="${1:-test-token}"
  local expires_at="${2:-$(($(date +%s) + 3600))}"
  local scope="${3:-}"

  local scope_field=""
  if [[ -n "$scope" ]]; then
    scope_field="\"scope\": \"$scope\","
  fi

  cat > "$TEST_HOME/.config/basecamp/credentials.json" << EOF
{
  "access_token": "$access_token",
  "refresh_token": "test-refresh-token",
  $scope_field
  "expires_at": $expires_at
}
EOF
  chmod 600 "$TEST_HOME/.config/basecamp/credentials.json"
}

create_accounts() {
  cat > "$TEST_HOME/.config/basecamp/accounts.json" << 'EOF'
[
  {"id": 99999, "name": "Test Account", "href": "https://3.basecampapi.com/99999"}
]
EOF
}


# Mock helpers

mock_api_response() {
  local response="$1"
  export BCQ_MOCK_RESPONSE="$response"
}


# Utility

is_valid_json() {
  echo "$output" | jq . &>/dev/null
}
