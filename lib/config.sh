#!/usr/bin/env bash
# config.sh - Layered configuration system for bcq
#
# Config hierarchy (later overrides earlier):
#   1. ~/.config/basecamp/config.json (global)
#   2. .basecamp/config.json (local/per-directory)
#   3. Environment variables
#   4. Command-line flags


# Paths

BCQ_GLOBAL_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
BCQ_LOCAL_CONFIG_DIR=".basecamp"
BCQ_CONFIG_FILE="config.json"
BCQ_CREDENTIALS_FILE="credentials.json"
BCQ_CLIENT_FILE="client.json"
BCQ_ACCOUNTS_FILE="accounts.json"


# Config Loading

declare -A _BCQ_CONFIG

_load_config_file() {
  local file="$1"
  if [[ -f "$file" ]]; then
    debug "Loading config from $file"
    while IFS='=' read -r key value; do
      _BCQ_CONFIG["$key"]="$value"
    done < <(jq -r 'to_entries | .[] | "\(.key)=\(.value)"' "$file" 2>/dev/null || true)
  fi
}

load_config() {
  _BCQ_CONFIG=()

  # Layer 1: Global config
  _load_config_file "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE"

  # Layer 2: Local config (walk up directory tree)
  local dir="$PWD"
  local local_configs=()
  while [[ "$dir" != "/" ]]; do
    if [[ -f "$dir/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
      local_configs+=("$dir/$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE")
    fi
    dir="$(dirname "$dir")"
  done

  # Apply local configs from root to current (so closer overrides)
  for ((i=${#local_configs[@]}-1; i>=0; i--)); do
    _load_config_file "${local_configs[$i]}"
  done

  # Layer 3: Environment variables
  [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]] && _BCQ_CONFIG["account_id"]="$BASECAMP_ACCOUNT_ID" || true
  [[ -n "${BASECAMP_PROJECT_ID:-}" ]] && _BCQ_CONFIG["project_id"]="$BASECAMP_PROJECT_ID" || true
  [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]] && _BCQ_CONFIG["access_token"]="$BASECAMP_ACCESS_TOKEN" || true

  # Layer 4: Command-line flags (already handled in global flag parsing)
  [[ -n "${BCQ_ACCOUNT:-}" ]] && _BCQ_CONFIG["account_id"]="$BCQ_ACCOUNT" || true
  [[ -n "${BCQ_PROJECT:-}" ]] && _BCQ_CONFIG["project_id"]="$BCQ_PROJECT" || true
}

get_config() {
  local key="$1"
  local default="${2:-}"
  echo "${_BCQ_CONFIG[$key]:-$default}"
}

has_config() {
  local key="$1"
  [[ -n "${_BCQ_CONFIG[$key]:-}" ]]
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

  _BCQ_CONFIG["$key"]="$value"
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

  _BCQ_CONFIG["$key"]="$value"
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

  unset "_BCQ_CONFIG[$key]"
}


# Credentials

get_credentials_path() {
  echo "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CREDENTIALS_FILE"
}

load_credentials() {
  local file
  file=$(get_credentials_path)
  if [[ -f "$file" ]]; then
    cat "$file"
  else
    echo '{}'
  fi
}

save_credentials() {
  local json="$1"
  ensure_global_config_dir
  local file
  file=$(get_credentials_path)
  echo "$json" > "$file"
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

load_accounts() {
  local file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_ACCOUNTS_FILE"
  if [[ -f "$file" ]]; then
    cat "$file"
  else
    echo '[]'
  fi
}

save_accounts() {
  local json="$1"
  ensure_global_config_dir
  echo "$json" > "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_ACCOUNTS_FILE"
}


# Config Display

get_effective_config() {
  local result='{}'
  for key in "${!_BCQ_CONFIG[@]}"; do
    if [[ "$key" == "access_token" ]] || [[ "$key" == "refresh_token" ]]; then
      result=$(echo "$result" | jq --arg key "$key" '.[$key] = "***"')
    else
      result=$(echo "$result" | jq --arg key "$key" --arg value "${_BCQ_CONFIG[$key]}" '.[$key] = $value')
    fi
  done
  echo "$result"
}

get_config_source() {
  local key="$1"

  case "$key" in
    account_id) [[ -n "${BCQ_ACCOUNT:-}" ]] && echo "flag" && return ;;
    project_id) [[ -n "${BCQ_PROJECT:-}" ]] && echo "flag" && return ;;
  esac

  case "$key" in
    account_id) [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]] && echo "env" && return ;;
    project_id) [[ -n "${BASECAMP_PROJECT_ID:-}" ]] && echo "env" && return ;;
    access_token) [[ -n "${BASECAMP_ACCESS_TOKEN:-}" ]] && echo "env" && return ;;
  esac

  if [[ -f "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local local_value
    local_value=$(jq -r --arg key "$key" '.[$key] // empty' "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$local_value" ]] && echo "local" && return
  fi

  if [[ -f "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    local global_value
    global_value=$(jq -r --arg key "$key" '.[$key] // empty' "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CONFIG_FILE" 2>/dev/null)
    [[ -n "$global_value" ]] && echo "global" && return
  fi

  echo "unset"
}


# Initialize

load_config
