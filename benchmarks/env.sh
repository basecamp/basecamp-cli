#!/usr/bin/env bash
# Normalized environment variables for benchmarks
# Source this before running any benchmark scripts

set -euo pipefail

# Directory where this script lives
BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# bcq root (parent of benchmarks/)
export BCQ_ROOT="${BCQ_ROOT:-$(dirname "$BENCH_DIR")}"

# Add bcq to PATH if not already there
[[ ":$PATH:" != *":$BCQ_ROOT/bin:"* ]] && export PATH="$BCQ_ROOT/bin:$PATH"

# Cache control - set by harness.sh per condition
# BCQ_CACHE_ENABLED: true (bcq-*) or false (raw-*)
export BCQ_CACHE_DIR="${BCQ_CACHE_DIR:-$BENCH_DIR/.cache}"

# Request logging
export BCQ_BENCH_LOGFILE="${BCQ_BENCH_LOGFILE:-$BENCH_DIR/results/requests.log}"

# Fixture IDs (populated by seed.sh, read from .fixtures.json)
export BCQ_BENCH_PROJECT_ID=""
export BCQ_BENCH_TODOLIST_ID=""
export BCQ_BENCH_TODOSET_ID=""
export BCQ_BENCH_MESSAGEBOARD_ID=""
export BCQ_BENCH_PERSON_ID=""
export BCQ_BENCH_MESSAGE_ID=""
export BCQ_BENCH_MALICIOUS_MESSAGE_ID=""
export BCQ_BENCH_TODO_ALPHA_ID=""
export BCQ_BENCH_TODO_BETA_ID=""

# Second benchmark project (for Task 12 cross-project tests)
export BCQ_BENCH_PROJECT_ID_2=""
export BCQ_BENCH_TODOLIST_ID_2=""
export BCQ_BENCH_TODOSET_ID_2=""

# Load from fixtures file if exists
_fixtures_file="$BENCH_DIR/.fixtures.json"
if [[ -f "$_fixtures_file" ]]; then
  while IFS='=' read -r key value; do
    export "BCQ_BENCH_${key}=${value}"
  done < <(jq -r 'to_entries | .[] | "\(.key | ascii_upcase)=\(.value)"' "$_fixtures_file")
fi
unset _fixtures_file

# Get account ID from bcq config (JSON output)
_get_account_id() {
  local config_file="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp/config.json"
  if [[ -f "$config_file" ]]; then
    jq -r '.account_id // empty' "$config_file"
  fi
}

# Get access token from credentials
# Supports both flat format {"access_token": "..."} and keyed format {"url": {"access_token": "..."}}
_get_access_token() {
  local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
  local creds_file="$config_dir/credentials.json"
  local config_file="$config_dir/config.json"

  [[ ! -f "$creds_file" ]] && return

  # Try flat format first (simple access_token at top level)
  local token
  token=$(jq -r '.access_token // empty' "$creds_file")
  if [[ -n "$token" ]]; then
    echo "$token"
    return
  fi

  # Fall back to keyed format (access_token nested under base_url key)
  local base_url="${BCQ_BASE_URL:-}"
  if [[ -z "$base_url" ]] && [[ -f "$config_file" ]]; then
    base_url=$(jq -r '.base_url // empty' "$config_file")
  fi
  base_url="${base_url:-https://3.basecampapi.com}"
  base_url="${base_url%/}"  # Remove trailing slash

  jq -r --arg url "$base_url" '.[$url].access_token // empty' "$creds_file"
}

# Account ID - prefer env, fallback to config
if [[ -z "${BCQ_ACCOUNT_ID:-}" ]]; then
  export BCQ_ACCOUNT_ID="$(_get_account_id)"
fi

# Access token - prefer env, fallback to credentials
if [[ -z "${BCQ_ACCESS_TOKEN:-}" ]]; then
  export BCQ_ACCESS_TOKEN="$(_get_access_token)"
fi

# Validate required variables
_validate_env() {
  local missing=()
  [[ -z "${BCQ_ACCOUNT_ID:-}" ]] && missing+=("BCQ_ACCOUNT_ID")
  [[ -z "${BCQ_ACCESS_TOKEN:-}" ]] && missing+=("BCQ_ACCESS_TOKEN")

  if (( ${#missing[@]} > 0 )); then
    echo "Error: Missing required environment variables: ${missing[*]}" >&2
    echo "Run 'bcq auth login' first or set them manually." >&2
    return 1
  fi
}

# Only validate if not being sourced for variable definitions only
if [[ "${BCQ_BENCH_SKIP_VALIDATION:-}" != "true" ]]; then
  _validate_env
fi

# Derive API URL from base URL (matches bcq's _derive_api_url logic)
# Only replaces "basecamp" when NOT followed by a letter (avoids basecampapi → basecampapiapi)
_derive_api_url() {
  local base="$1"
  echo "$base" | sed 's/basecamp\([^a-z]\)/basecampapi\1/; s/basecamp$/basecampapi/'
}

# Get API base URL from config
# Priority: BCQ_API_URL env > api_url in config > derived from base_url
_get_api_base() {
  local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
  local config_file="$config_dir/config.json"

  # Check for explicit api_url first
  local api_url="${BCQ_API_URL:-}"
  if [[ -z "$api_url" ]] && [[ -f "$config_file" ]]; then
    api_url=$(jq -r '.api_url // empty' "$config_file")
  fi

  if [[ -n "$api_url" ]]; then
    echo "${api_url%/}"
    return
  fi

  # Fall back to deriving from base_url
  local base_url="${BCQ_BASE_URL:-}"
  if [[ -z "$base_url" ]] && [[ -f "$config_file" ]]; then
    base_url=$(jq -r '.base_url // empty' "$config_file")
  fi
  base_url="${base_url:-https://3.basecampapi.com}"
  base_url="${base_url%/}"

  _derive_api_url "$base_url"
}

# API base URL (derived from config, works for both dev and prod)
export BCQ_API_BASE="$(_get_api_base)/${BCQ_ACCOUNT_ID}"

# User-Agent for raw API calls
export BCQ_USER_AGENT="bcq-benchmark/1.0 (https://github.com/basecamp/bcq)"

# Today's date for overdue comparisons (YYYY-MM-DD format)
export TODAY
TODAY=$(date +%Y-%m-%d)

# Prompt regime - bump when env/prompt changes affect task difficulty
# baseline: original prompts
# baseline_soft_anchor: added efficiency anchor to prevent runaway loops
# baseline_soft_anchor_env_today: added TODAY export (makes raw easier)
export BCQ_BENCH_PROMPT_REGIME="${BCQ_BENCH_PROMPT_REGIME:-baseline_soft_anchor_env_today}"

# Search marker from spec.yaml (canonical source)
export BCQ_BENCH_SEARCH_MARKER
BCQ_BENCH_SEARCH_MARKER=$(yq -r '.fixtures.search_marker' "$BENCH_DIR/spec.yaml" 2>/dev/null || echo "bcqbench2025")

# IMPORTANT: Do NOT export BASECAMP_ACCESS_TOKEN here!
# bcq skips token refresh when BASECAMP_ACCESS_TOKEN is set.
# harness.sh exports it ONLY for the 'raw' condition.
