#!/usr/bin/env bats
# config.bats - Tests for lib/config.sh

load test_helper


# Config loading

@test "loads global config" {
  create_global_config '{"account_id": "12345"}'

  # Source the lib directly to test
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "account_id")
  [[ "$result" == "12345" ]]
}

@test "loads local config" {
  create_local_config '{"project_id": "67890"}'

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "project_id")
  [[ "$result" == "67890" ]]
}

@test "local config overrides global config" {
  create_global_config '{"project_id": "global-123"}'
  create_local_config '{"project_id": "local-456"}'

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "project_id")
  [[ "$result" == "local-456" ]]
}

@test "environment variable overrides config file" {
  create_global_config '{"account_id": "from-file"}'
  export BASECAMP_ACCOUNT_ID="from-env"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "account_id")
  [[ "$result" == "from-env" ]]
}


# Config defaults

@test "get_config returns default for missing key" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "nonexistent" "default-value")
  [[ "$result" == "default-value" ]]
}

@test "has_config returns false for missing key" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  ! has_config "nonexistent"
}

@test "has_config returns true for existing key" {
  create_global_config '{"account_id": "12345"}'

  source "$BCQ_ROOT/lib/core.sh"
  BCQ_GLOBAL_CONFIG_DIR="$HOME/.config/basecamp"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  has_config "account_id"
}


# Credentials

@test "loads credentials from file" {
  create_credentials "my-test-token" "$(($(date +%s) + 3600))"
  unset BASECAMP_ACCESS_TOKEN

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  result=$(get_access_token)
  [[ "$result" == "my-test-token" ]]
}

@test "environment token overrides file" {
  create_credentials "file-token"
  export BASECAMP_ACCESS_TOKEN="env-token"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  result=$(get_access_token)
  [[ "$result" == "env-token" ]]
}

@test "is_token_expired returns true for expired token" {
  create_credentials "test-token" "$(($(date +%s) - 100))"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  is_token_expired
}

@test "is_token_expired returns false for valid token" {
  create_credentials "test-token" "$(($(date +%s) + 3600))"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  ! is_token_expired
}


# Account/Project getters

@test "get_account_id from config" {
  create_global_config '{"account_id": "99999"}'
  unset BASECAMP_ACCOUNT_ID
  unset BCQ_ACCOUNT

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_account_id)
  [[ "$result" == "99999" ]]
}

@test "get_project_id from config" {
  create_local_config '{"project_id": "88888"}'
  unset BASECAMP_PROJECT_ID
  unset BCQ_PROJECT

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_project_id)
  [[ "$result" == "88888" ]]
}
