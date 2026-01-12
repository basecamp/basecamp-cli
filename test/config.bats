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


# API URL configuration

@test "loads base_url from config" {
  create_global_config '{"base_url": "http://dev.example.com"}'
  unset BCQ_BASE_URL
  unset BCQ_API_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_BASE_URL" == "http://dev.example.com" ]]
}

@test "loads api_url from config" {
  create_global_config '{"api_url": "http://api.example.com"}'
  unset BCQ_BASE_URL
  unset BCQ_API_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_API_URL" == "http://api.example.com" ]]
}

@test "environment BCQ_BASE_URL overrides config" {
  create_global_config '{"base_url": "http://from-config.com"}'
  export BCQ_BASE_URL="http://from-env.com"
  unset BCQ_API_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_BASE_URL" == "http://from-env.com" ]]
}

@test "environment BCQ_API_URL overrides config" {
  create_global_config '{"api_url": "http://from-config.com"}'
  export BCQ_API_URL="http://from-env.com"
  unset BCQ_BASE_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_API_URL" == "http://from-env.com" ]]
}

@test "defaults to production when no config" {
  unset BCQ_BASE_URL
  unset BCQ_API_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_BASE_URL" == "https://3.basecampapi.com" ]]
  [[ "$BCQ_API_URL" == "https://3.basecampapi.com" ]]
}

@test "derives api_url from base_url" {
  create_global_config '{"base_url": "http://3.basecamp.localhost:3001"}'
  unset BCQ_BASE_URL
  unset BCQ_API_URL

  source "$BCQ_ROOT/lib/core.sh"

  [[ "$BCQ_BASE_URL" == "http://3.basecamp.localhost:3001" ]]
  [[ "$BCQ_API_URL" == "http://3.basecampapi.localhost:3001" ]]
}
