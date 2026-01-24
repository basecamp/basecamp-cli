#!/usr/bin/env bash
# config.sh - Layered configuration system for bcq
#
# Config hierarchy (later overrides earlier):
#   1. /etc/basecamp/config.json (system-wide)
#   2. ~/.config/basecamp/config.json (user/global)
#   3. <git-root>/.basecamp/config.json (repo)
#   4. <cwd>/.basecamp/config.json (local, walks up tree)
#   5. Environment variables
#   6. Command-line flags


# Paths

BCQ_SYSTEM_CONFIG_DIR="${BCQ_SYSTEM_CONFIG_DIR:-/etc/basecamp}"
BCQ_GLOBAL_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
BCQ_LOCAL_CONFIG_DIR=".basecamp"
BCQ_CONFIG_FILE="config.json"
BCQ_CREDENTIALS_FILE="credentials.json"
BCQ_CLIENT_FILE="client.json"
BCQ_ACCOUNTS_FILE="accounts.json"


# Config Loading
#
# Uses indexed array with "key=value" strings for Bash 3.2 compatibility
# (macOS ships with Bash 3.2, associative arrays require Bash 4+)

_BCQ_CONFIG=()

# Internal helpers for config key-value storage
# Named with __cfg prefix to avoid conflicts with command functions
__cfg_set() {
  local key="$1" value="$2"
  __cfg_unset "$key"
  _BCQ_CONFIG+=("$key=$value")
}

__cfg_get() {
  local key="$1" default="${2:-}"
  local entry value
  for entry in "${_BCQ_CONFIG[@]+"${_BCQ_CONFIG[@]}"}"; do
    if [[ "${entry%%=*}" == "$key" ]]; then
      value="${entry#*=}"
      # Return default for empty values (matches old :- expansion behavior)
      if [[ -n "$value" ]]; then
        printf '%s\n' "$value"
      else
        printf '%s\n' "$default"
      fi
      return
    fi
  done
  printf '%s\n' "$default"
}

__cfg_has() {
  local key="$1" entry value
  for entry in "${_BCQ_CONFIG[@]+"${_BCQ_CONFIG[@]}"}"; do
    if [[ "${entry%%=*}" == "$key" ]]; then
      value="${entry#*=}"
      [[ -n "$value" ]] && return 0
      return 1
    fi
  done
  return 1
}

__cfg_unset() {
  local key="$1"
  local new_config=() entry
  for entry in "${_BCQ_CONFIG[@]+"${_BCQ_CONFIG[@]}"}"; do
    [[ "${entry%%=*}" != "$key" ]] && new_config+=("$entry")
  done
  _BCQ_CONFIG=("${new_config[@]+"${new_config[@]}"}")
}

__cfg_keys() {
  local entry
  for entry in "${_BCQ_CONFIG[@]+"${_BCQ_CONFIG[@]}"}"; do
    echo "${entry%%=*}"
  done
}

_load_config_file() {
  local file="$1"
  if [[ -f "$file" ]]; then
    debug "Loading config from $file"
    while IFS='=' read -r key value; do
      __cfg_set "$key" "$value"
    done < <(jq -r 'to_entries | .[] | "\(.key)=\(.value)"' "$file" 2>/dev/null || true)
  fi
}

load_config() {
  _BCQ_CONFIG=()

  # Layer 1: System-wide config
  _load_config_file "$BCQ_SYSTEM_CONFIG_DIR/$BCQ_CONFIG_FILE"

  # Layer 2: User/global config
  _load_config_file "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE"

  # Layer 3: Git repo root config (if in a git repo)
  local git_root
  git_root=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [[ -n "$git_root" ]] && [[ -f "$git_root/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    _load_config_file "$git_root/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
  fi

  # Layer 4: Local config (walk up directory tree, skip git root if already loaded)
  local dir="$PWD"
  local local_configs=()
  while [[ "$dir" != "/" ]]; do
    local config_path="$dir/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
    # Skip if this is the git root (already loaded above)
    if [[ -f "$config_path" ]] && [[ "$dir" != "$git_root" ]]; then
      local_configs+=("$config_path")
    fi
    dir="$(dirname "$dir")"
  done

  # Apply local configs from root to current (so closer overrides)
  for ((i=${#local_configs[@]}-1; i>=0; i--)); do
    _load_config_file "${local_configs[$i]}"
  done

  # Layer 5: Environment variables
  [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]] && __cfg_set "account_id" "$BASECAMP_ACCOUNT_ID" || true
  [[ -n "${BASECAMP_PROJECT_ID:-}" ]] && __cfg_set "project_id" "$BASECAMP_PROJECT_ID" || true
  [[ -n "${BASECAMP_TODOLIST_ID:-}" ]] && __cfg_set "todolist_id" "$BASECAMP_TODOLIST_ID" || true
  [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]] && __cfg_set "access_token" "$BASECAMP_ACCESS_TOKEN" || true

  # Layer 6: Command-line flags (already handled in global flag parsing)
  [[ -n "${BCQ_ACCOUNT:-}" ]] && __cfg_set "account_id" "$BCQ_ACCOUNT" || true
  [[ -n "${BCQ_PROJECT:-}" ]] && __cfg_set "project_id" "$BCQ_PROJECT" || true
  [[ -n "${BCQ_CACHE_DIR:-}" ]] && __cfg_set "cache_dir" "$BCQ_CACHE_DIR" || true
}

get_config() {
  local key="$1"
  local default="${2:-}"
  __cfg_get "$key" "$default"
}

has_config() {
  local key="$1"
  __cfg_has "$key"
}


# Config Writing

ensure_global_config_dir() {
  mkdir -p "$BCQ_GLOBAL_CONFIG_DIR"
}

ensure_local_config_dir() {
  mkdir -p "$BCQ_LOCAL_CONFIG_DIR"
}

set_global_config() {
  local key="$1"
  local value="$2"

  ensure_global_config_dir
  local file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE"

  if [[ -f "$file" ]]; then
    local tmp
    tmp=$(mktemp)
    jq --arg key "$key" --arg value "$value" '.[$key] = $value' "$file" > "$tmp"
    mv "$tmp" "$file"
  else
    jq -n --arg key "$key" --arg value "$value" '{($key): $value}' > "$file"
  fi

  __cfg_set "$key" "$value"
}

set_local_config() {
  local key="$1"
  local value="$2"

  ensure_local_config_dir
  local file="$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"

  if [[ -f "$file" ]]; then
    local tmp
    tmp=$(mktemp)
    jq --arg key "$key" --arg value "$value" '.[$key] = $value' "$file" > "$tmp"
    mv "$tmp" "$file"
  else
    jq -n --arg key "$key" --arg value "$value" '{($key): $value}' > "$file"
  fi

  __cfg_set "$key" "$value"
}

unset_config() {
  local key="$1"
  local scope="${2:---local}"

  local file
  if [[ "$scope" == "--global" ]]; then
    file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
  else
    file="$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
  fi

  if [[ -f "$file" ]]; then
    local tmp
    tmp=$(mktemp)
    jq --arg key "$key" 'del(.[$key])' "$file" > "$tmp"
    mv "$tmp" "$file"
  fi

  __cfg_unset "$key"
}


# Multi-Origin Helpers
#
# Credentials are keyed by base URL to support multiple Basecamp instances
# (production vs local development).

_normalize_base_url() {
  # Remove trailing slash for consistent keys
  local url="${1:-$BCQ_BASE_URL}"
  echo "${url%/}"
}


# Credentials

get_credentials_path() {
  echo "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CREDENTIALS_FILE"
}

load_credentials() {
  local file
  file=$(get_credentials_path)
  local base_url
  base_url=$(_normalize_base_url)

  if [[ ! -f "$file" ]]; then
    echo '{}'
    return
  fi

  # Return credentials for current base URL
  jq -r --arg url "$base_url" '.[$url] // {}' "$file"
}

save_credentials() {
  local json="$1"
  ensure_global_config_dir
  local file
  file=$(get_credentials_path)
  local base_url
  base_url=$(_normalize_base_url)

  # Load existing multi-origin credentials
  local existing='{}'
  if [[ -f "$file" ]]; then
    existing=$(cat "$file")
  fi

  # Update credentials for current base URL
  local updated
  updated=$(echo "$existing" | jq --arg url "$base_url" --argjson creds "$json" '.[$url] = $creds')
  echo "$updated" > "$file"
  chmod 600 "$file"
}

clear_credentials() {
  local file
  file=$(get_credentials_path)
  local base_url
  base_url=$(_normalize_base_url)

  if [[ ! -f "$file" ]]; then
    return
  fi

  # Remove credentials for current base URL only
  local updated
  updated=$(jq --arg url "$base_url" 'del(.[$url])' "$file")
  echo "$updated" > "$file"
  chmod 600 "$file"
}

get_access_token() {
  if [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]]; then
    echo "$BASECAMP_ACCESS_TOKEN"
    return
  fi

  local creds
  creds=$(load_credentials)
  local token
  token=$(echo "$creds" | jq -r '.access_token // empty')

  if [[ -z "$token" ]]; then
    return 1
  fi

  echo "$token"
}

is_token_expired() {
  local creds
  creds=$(load_credentials)
  local expires_at
  expires_at=$(echo "$creds" | jq -r '.expires_at // 0')

  local now
  now=$(date +%s)
  (( now > expires_at - 60 ))
}

get_token_scope() {
  local creds
  creds=$(load_credentials)
  local scope
  scope=$(echo "$creds" | jq -r '.scope // empty')

  if [[ -z "$scope" ]]; then
    echo "unknown"
    return 1
  fi

  echo "$scope"
}


# Account Management

get_account_id() {
  if [[ -n "${BCQ_ACCOUNT:-}" ]]; then
    echo "$BCQ_ACCOUNT"
    return
  fi
  if [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]]; then
    echo "$BASECAMP_ACCOUNT_ID"
    return
  fi
  get_config "account_id"
}

get_project_id() {
  if [[ -n "${BCQ_PROJECT:-}" ]]; then
    echo "$BCQ_PROJECT"
    return
  fi
  if [[ -n "${BASECAMP_PROJECT_ID:-}" ]]; then
    echo "$BASECAMP_PROJECT_ID"
    return
  fi
  get_config "project_id"
}

get_todolist_id() {
  if [[ -n "${BASECAMP_TODOLIST_ID:-}" ]]; then
    echo "$BASECAMP_TODOLIST_ID"
    return
  fi
  get_config "todolist_id"
}

get_git_root() {
  git rev-parse --show-toplevel 2>/dev/null || true
}

load_accounts() {
  local file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_ACCOUNTS_FILE"
  local base_url
  base_url=$(_normalize_base_url)

  if [[ ! -f "$file" ]]; then
    echo '[]'
    return
  fi

  # Return accounts for current base URL
  jq -r --arg url "$base_url" '.[$url] // []' "$file"
}

save_accounts() {
  local json="$1"
  ensure_global_config_dir
  local file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_ACCOUNTS_FILE"
  local base_url
  base_url=$(_normalize_base_url)

  # Load existing multi-origin accounts
  local existing='{}'
  if [[ -f "$file" ]]; then
    existing=$(cat "$file")
    # Handle legacy array format (migrate to object)
    if echo "$existing" | jq -e 'type == "array"' >/dev/null 2>&1; then
      existing='{}'
    fi
  fi

  # Update accounts for current base URL
  local updated
  updated=$(echo "$existing" | jq --arg url "$base_url" --argjson accts "$json" '.[$url] = $accts')
  echo "$updated" > "$file"
}


# ============================================================================
# JSON-First Config API (New)
#
# These functions provide a cleaner API with explicit source tracking.
# The merged config and sources are stored as JSON for type safety.
# ============================================================================

BCQ_CONFIG_JSON='{}'
BCQ_CONFIG_SOURCES_JSON='{}'

# Helper: Read and validate a JSON config file
# Returns valid JSON or '{}' if file is missing/invalid
_read_json_config() {
  local file="$1"
  if [[ -f "$file" ]]; then
    local content
    content=$(cat "$file" 2>/dev/null) || { echo '{}'; return; }
    # Validate JSON, fall back to {} if malformed
    if echo "$content" | jq -e . >/dev/null 2>&1; then
      echo "$content"
    else
      debug "Warning: invalid JSON in $file, skipping"
      echo '{}'
    fi
  else
    echo '{}'
  fi
}

# Load all config layers and merge into BCQ_CONFIG_JSON
# Also builds BCQ_CONFIG_SOURCES_JSON with source tracking
config_load() {
  local system_json='{}'
  local global_json='{}'
  local repo_json='{}'
  local local_json='{}'
  local env_json='{}'
  local flag_json='{}'

  # Layer 1: System-wide config (lowest priority)
  system_json=$(_read_json_config "$BCQ_SYSTEM_CONFIG_DIR/$BCQ_CONFIG_FILE")

  # Layer 2: User/global config
  global_json=$(_read_json_config "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE")

  # Layer 3: Git repo root config
  local git_root
  git_root=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [[ -n "$git_root" ]]; then
    repo_json=$(_read_json_config "$git_root/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE")
  fi

  # Layer 4: Local configs (walk up cwd tree, excluding git root)
  # Collect all local configs then merge from root to cwd (closer overrides)
  local dir="$PWD"
  local local_configs=()
  while [[ "$dir" != "/" ]]; do
    local config_path="$dir/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
    if [[ -f "$config_path" ]] && [[ "$dir" != "$git_root" ]]; then
      local_configs+=("$config_path")
    fi
    dir="$(dirname "$dir")"
  done

  # Merge local configs from root to cwd (reverse order, so closer overrides)
  local_json='{}'
  for ((i=${#local_configs[@]}-1; i>=0; i--)); do
    local layer_json
    layer_json=$(_read_json_config "${local_configs[$i]}")
    local_json=$(printf '%s\n%s' "$local_json" "$layer_json" | jq -s '.[0] * .[1]')
  done

  # Layer 5: Environment variables
  env_json=$(jq -n \
    --arg account_id "${BASECAMP_ACCOUNT_ID:-}" \
    --arg project_id "${BASECAMP_PROJECT_ID:-}" \
    --arg todolist_id "${BASECAMP_TODOLIST_ID:-}" \
    --arg access_token "${BASECAMP_ACCESS_TOKEN:-}" \
    '{} |
      if $account_id != "" then .account_id = $account_id else . end |
      if $project_id != "" then .project_id = $project_id else . end |
      if $todolist_id != "" then .todolist_id = $todolist_id else . end |
      if $access_token != "" then .access_token = $access_token else . end')

  # Layer 6: Command-line flags (highest priority)
  flag_json=$(jq -n \
    --arg account_id "${BCQ_ACCOUNT:-}" \
    --arg project_id "${BCQ_PROJECT:-}" \
    --arg cache_dir "${BCQ_CACHE_DIR:-}" \
    '{} |
      if $account_id != "" then .account_id = $account_id else . end |
      if $project_id != "" then .project_id = $project_id else . end |
      if $cache_dir != "" then .cache_dir = $cache_dir else . end')

  # Merge layers (later overrides earlier)
  # Use printf with newlines so jq -s can parse multiple JSON objects correctly
  BCQ_CONFIG_JSON=$(printf '%s\n' "$system_json" "$global_json" "$repo_json" "$local_json" "$env_json" "$flag_json" | \
    jq -s 'reduce .[] as $item ({}; . * $item)')

  # Build sources map by walking layers from highest to lowest priority
  # First key to set a value wins (since we go high to low)
  BCQ_CONFIG_SOURCES_JSON=$(jq -n \
    --argjson flag "$flag_json" \
    --argjson env "$env_json" \
    --argjson local "$local_json" \
    --argjson repo "$repo_json" \
    --argjson global "$global_json" \
    --argjson system "$system_json" \
    '
    def add_sources(obj; source):
      reduce (obj | keys[]) as $k (.; if has($k) | not then .[$k] = source else . end);

    {} |
    add_sources($flag; "flag") |
    add_sources($env; "env") |
    add_sources($local; "local") |
    add_sources($repo; "repo") |
    add_sources($global; "global") |
    add_sources($system; "system")
    ')
}

# Get a config value from the merged JSON config
# Args: $1 - key, $2 - default (optional)
# Returns: value or default
config_get_json() {
  local key="$1"
  local default="${2:-}"
  local value
  value=$(echo "$BCQ_CONFIG_JSON" | jq -r --arg k "$key" '.[$k] // empty')
  if [[ -n "$value" ]]; then
    echo "$value"
  else
    echo "$default"
  fi
}

# Check if a config key exists and has a non-null value
# Args: $1 - key
# Returns: 0 if exists, 1 if not
config_has_json() {
  local key="$1"
  local value
  value=$(echo "$BCQ_CONFIG_JSON" | jq -r --arg k "$key" '.[$k] // empty')
  [[ -n "$value" ]]
}

# Get the source of a config value
# Args: $1 - key
# Returns: source name (flag, env, local, repo, global, system, unset)
config_source_json() {
  local key="$1"
  local source
  source=$(echo "$BCQ_CONFIG_SOURCES_JSON" | jq -r --arg k "$key" '.[$k] // "unset"')
  echo "$source"
}

# Get all config with sources (for display)
# Returns: JSON object with {key: {value: ..., source: ...}, ...}
config_show_all() {
  jq -n \
    --argjson config "$BCQ_CONFIG_JSON" \
    --argjson sources "$BCQ_CONFIG_SOURCES_JSON" \
    '
    $config | to_entries | map({
      key: .key,
      value: {
        value: (if .key == "access_token" or .key == "refresh_token" then "***" else .value end),
        source: ($sources[.key] // "unset")
      }
    }) | from_entries
    '
}


# Config Display (Legacy)

get_effective_config() {
  local result='{}' key value
  while IFS= read -r key; do
    [[ -z "$key" ]] && continue
    value=$(__cfg_get "$key")
    if [[ "$key" == "access_token" ]] || [[ "$key" == "refresh_token" ]]; then
      result=$(echo "$result" | jq --arg key "$key" '.[$key] = "***"')
    else
      result=$(echo "$result" | jq --arg key "$key" --arg value "$value" '.[$key] = $value')
    fi
  done < <(__cfg_keys)
  echo "$result"
}

get_config_source() {
  local key="$1"

  # Check flags first (highest priority)
  case "$key" in
    account_id) [[ -n "${BCQ_ACCOUNT:-}" ]] && echo "flag" && return ;;
    project_id) [[ -n "${BCQ_PROJECT:-}" ]] && echo "flag" && return ;;
  esac

  # Check environment variables
  case "$key" in
    account_id) [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]] && echo "env" && return ;;
    project_id) [[ -n "${BASECAMP_PROJECT_ID:-}" ]] && echo "env" && return ;;
    todolist_id) [[ -n "${BASECAMP_TODOLIST_ID:-}" ]] && echo "env" && return ;;
    access_token) [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]] && echo "env" && return ;;
  esac

  # Check local (cwd) config
  if [[ -f "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local local_value
    local_value=$(jq -r --arg key "$key" '.[$key] // empty' "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$local_value" ]] && echo "local (.basecamp/)" && return
  fi

  # Check git repo root config
  local git_root
  git_root=$(get_git_root)
  if [[ -n "$git_root" ]] && [[ -f "$git_root/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local repo_value
    repo_value=$(jq -r --arg key "$key" '.[$key] // empty' "$git_root/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$repo_value" ]] && echo "repo ($git_root/.basecamp/)" && return
  fi

  # Check user/global config
  if [[ -f "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local global_value
    global_value=$(jq -r --arg key "$key" '.[$key] // empty' "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$global_value" ]] && echo "user (~/.config/basecamp/)" && return
  fi

  # Check system-wide config
  if [[ -f "$BCQ_SYSTEM_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local system_value
    system_value=$(jq -r --arg key "$key" '.[$key] // empty' "$BCQ_SYSTEM_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$system_value" ]] && echo "system (/etc/basecamp/)" && return
  fi

  echo "unset"
}


# Initialize

load_config
config_load
