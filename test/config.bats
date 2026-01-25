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

@test "has_config returns false for empty value" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "empty_key" ""
  ! has_config "empty_key"
}

@test "get_config returns default for empty value" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "empty_key" ""
  result=$(__cfg_get "empty_key" "fallback")
  [[ "$result" == "fallback" ]]
}


# Config round-trip (Bash 3.2 storage)

@test "config set/get round-trip preserves value" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "test_key" "test_value"
  result=$(__cfg_get "test_key")
  [[ "$result" == "test_value" ]]
}

@test "config unset removes key" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "temp_key" "temp_value"
  __cfg_unset "temp_key"
  result=$(__cfg_get "temp_key" "default")
  [[ "$result" == "default" ]]
}

@test "config handles values starting with -n" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "flag_key" "-n some value"
  result=$(__cfg_get "flag_key")
  [[ "$result" == "-n some value" ]]
}

@test "config handles values starting with -e" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "escape_key" "-e test\nvalue"
  result=$(__cfg_get "escape_key")
  [[ "$result" == "-e test\nvalue" ]]
}

@test "config set overwrites existing key" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  __cfg_set "overwrite_key" "first"
  __cfg_set "overwrite_key" "second"
  result=$(__cfg_get "overwrite_key")
  [[ "$result" == "second" ]]
}


# Credentials

@test "loads credentials from file" {
  create_credentials "my-test-token" "$(($(date +%s) + 3600))"
  unset BASECAMP_TOKEN

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  result=$(get_access_token)
  [[ "$result" == "my-test-token" ]]
}

@test "environment token overrides file" {
  create_credentials "file-token"
  export BASECAMP_TOKEN="env-token"

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


# Config Layering

@test "loads system-wide config" {
  create_system_config '{"account_id": "system-123"}'

  source "$BCQ_ROOT/lib/core.sh"
  BCQ_SYSTEM_CONFIG_DIR="$TEST_TEMP_DIR/etc/basecamp"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "account_id")
  [[ "$result" == "system-123" ]]
}

@test "user config overrides system config" {
  create_system_config '{"account_id": "system-123"}'
  create_global_config '{"account_id": "user-456"}'

  source "$BCQ_ROOT/lib/core.sh"
  BCQ_SYSTEM_CONFIG_DIR="$TEST_TEMP_DIR/etc/basecamp"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "account_id")
  [[ "$result" == "user-456" ]]
}

@test "repo config detected from git root" {
  init_git_repo "$TEST_PROJECT"
  mkdir -p "$TEST_PROJECT/subdir"
  create_repo_config '{"project_id": "repo-789"}' "$TEST_PROJECT"

  cd "$TEST_PROJECT/subdir"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "project_id")
  [[ "$result" == "repo-789" ]]
}

@test "repo config overrides user config" {
  init_git_repo "$TEST_PROJECT"
  create_global_config '{"project_id": "user-config"}'
  create_repo_config '{"project_id": "repo-config"}' "$TEST_PROJECT"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "project_id")
  [[ "$result" == "repo-config" ]]
}

@test "local config overrides repo config" {
  init_git_repo "$TEST_PROJECT"
  mkdir -p "$TEST_PROJECT/subdir/.basecamp"
  create_repo_config '{"project_id": "repo-config"}' "$TEST_PROJECT"
  echo '{"project_id": "local-config"}' > "$TEST_PROJECT/subdir/.basecamp/config.json"

  cd "$TEST_PROJECT/subdir"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config "project_id")
  [[ "$result" == "local-config" ]]
}

@test "full config layering priority" {
  # Set up all 6 layers
  create_system_config '{"account_id": "system", "project_id": "system", "todolist_id": "system"}'
  create_global_config '{"account_id": "user", "project_id": "user", "todolist_id": "user"}'
  init_git_repo "$TEST_PROJECT"
  create_repo_config '{"account_id": "repo", "project_id": "repo", "todolist_id": "repo"}' "$TEST_PROJECT"
  mkdir -p "$TEST_PROJECT/subdir/.basecamp"
  echo '{"project_id": "local", "todolist_id": "local"}' > "$TEST_PROJECT/subdir/.basecamp/config.json"
  export BASECAMP_TODOLIST_ID="env"

  cd "$TEST_PROJECT/subdir"

  source "$BCQ_ROOT/lib/core.sh"
  BCQ_SYSTEM_CONFIG_DIR="$TEST_TEMP_DIR/etc/basecamp"
  source "$BCQ_ROOT/lib/config.sh"

  load_config

  # account_id: local doesn't set, env doesn't set, so repo wins
  result=$(get_config "account_id")
  [[ "$result" == "repo" ]]

  # project_id: local sets it
  result=$(get_config "project_id")
  [[ "$result" == "local" ]]

  # todolist_id: env overrides all files
  result=$(get_config "todolist_id")
  [[ "$result" == "env" ]]
}


# Todolist ID getter

@test "get_todolist_id from config" {
  create_local_config '{"todolist_id": "77777"}'
  unset BASECAMP_TODOLIST_ID

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_todolist_id)
  [[ "$result" == "77777" ]]
}

@test "get_todolist_id from environment" {
  create_local_config '{"todolist_id": "from-file"}'
  export BASECAMP_TODOLIST_ID="from-env"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_todolist_id)
  [[ "$result" == "from-env" ]]
}


# Config source tracking

@test "get_config_source returns env for environment variable" {
  export BASECAMP_ACCOUNT_ID="from-env"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "account_id")
  [[ "$result" == "env" ]]
}

@test "get_config_source returns flag for BCQ_ACCOUNT" {
  export BCQ_ACCOUNT="from-flag"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "account_id")
  [[ "$result" == "flag" ]]
}

@test "get_config_source returns local for cwd config" {
  create_local_config '{"project_id": "from-local"}'

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "project_id")
  [[ "$result" == *"local"* ]]
}

@test "get_config_source returns user for global config" {
  create_global_config '{"account_id": "from-user"}'
  unset BASECAMP_ACCOUNT_ID
  unset BCQ_ACCOUNT

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "account_id")
  [[ "$result" == *"user"* ]]
}

@test "get_config_source returns system for system-wide config" {
  create_system_config '{"account_id": "from-system"}'
  unset BASECAMP_ACCOUNT_ID
  unset BCQ_ACCOUNT

  source "$BCQ_ROOT/lib/core.sh"
  BCQ_SYSTEM_CONFIG_DIR="$TEST_TEMP_DIR/etc/basecamp"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "account_id")
  [[ "$result" == *"system"* ]]
}

@test "get_config_source returns repo for git root config" {
  init_git_repo "$TEST_PROJECT"
  create_repo_config '{"project_id": "from-repo"}' "$TEST_PROJECT"
  mkdir -p "$TEST_PROJECT/subdir"

  cd "$TEST_PROJECT/subdir"

  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "project_id")
  [[ "$result" == *"repo"* ]]
}

@test "get_config_source returns unset for missing key" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"

  load_config
  result=$(get_config_source "nonexistent")
  [[ "$result" == "unset" ]]
}
