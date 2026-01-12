#!/usr/bin/env bash
# core.sh - Core utilities for bcq
# Output formatting, response envelope, global flags


# Environment Configuration
# BCQ_BASE_URL - Web app URL for OAuth flows (authorization, login)
# BCQ_API_URL  - API URL for resource access (projects, todos, etc.)
#
# In production, these are the same (3.basecampapi.com handles both).
# In development, they may differ:
#   BCQ_BASE_URL=http://3.basecamp.localhost:3001    (web app, has login cookies)
#   BCQ_API_URL=http://3.basecampapi.localhost:3001  (API host for untrusted clients)
#
# URL resolution priority:
#   1. Environment variables (BCQ_BASE_URL, BCQ_API_URL)
#   2. Stored config from auth (remembers which server issued the token)
#   3. Production defaults

# Derive API URL from BASE_URL if not set
# Replaces "basecamp" with "basecampapi" in the hostname
_derive_api_url() {
  local base="$1"
  echo "$base" | sed 's/basecamp\([^a-z]\)/basecampapi\1/; s/basecamp$/basecampapi/'
}

# Load URLs from config if not set via environment
# This happens early, before full config loading
_load_url_config() {
  local config_file="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp/config.json"
  if [[ -f "$config_file" ]]; then
    local base_url api_url
    base_url=$(jq -r '.base_url // empty' "$config_file" 2>/dev/null) || true
    api_url=$(jq -r '.api_url // empty' "$config_file" 2>/dev/null) || true
    if [[ -z "${BCQ_BASE_URL:-}" ]] && [[ -n "$base_url" ]]; then
      BCQ_BASE_URL="$base_url"
    fi
    if [[ -z "${BCQ_API_URL:-}" ]] && [[ -n "$api_url" ]]; then
      BCQ_API_URL="$api_url"
    fi
  fi
}

_load_url_config

# Apply defaults if still not set
BCQ_BASE_URL="${BCQ_BASE_URL:-https://3.basecampapi.com}"
BCQ_API_URL="${BCQ_API_URL:-$(_derive_api_url "$BCQ_BASE_URL")}"


# Date Parsing

parse_date() {
  local input="$1"

  case "$input" in
    today)
      date +%Y-%m-%d
      ;;
    tomorrow)
      date -v+1d +%Y-%m-%d 2>/dev/null || date -d "+1 day" +%Y-%m-%d
      ;;
    yesterday)
      date -v-1d +%Y-%m-%d 2>/dev/null || date -d "-1 day" +%Y-%m-%d
      ;;
    "next week"|nextweek)
      date -v+7d +%Y-%m-%d 2>/dev/null || date -d "+7 days" +%Y-%m-%d
      ;;
    "next month"|nextmonth)
      date -v+1m +%Y-%m-%d 2>/dev/null || date -d "+1 month" +%Y-%m-%d
      ;;
    +[0-9]*)
      # +N days format (e.g., +3 for 3 days from now)
      local days="${input#+}"
      date -v+"${days}"d +%Y-%m-%d 2>/dev/null || date -d "+${days} days" +%Y-%m-%d
      ;;
    [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9])
      # Already in YYYY-MM-DD format
      echo "$input"
      ;;
    *)
      # Try to parse with date command, otherwise return as-is
      date -j -f "%Y-%m-%d" "$input" +%Y-%m-%d 2>/dev/null || echo "$input"
      ;;
  esac
}


# Global State

BCQ_FORMAT="${BCQ_FORMAT:-auto}"    # Output format: auto, json, md
BCQ_QUIET="${BCQ_QUIET:-false}"     # Suppress non-essential output
BCQ_VERBOSE="${BCQ_VERBOSE:-false}" # Debug output
GLOBAL_FLAGS_CONSUMED=0              # For shift in main


# Output Format Detection

is_tty() {
  [[ -t 1 ]]
}

get_format() {
  case "$BCQ_FORMAT" in
    json) echo "json" ;;
    md|markdown) echo "md" ;;
    auto)
      if is_tty; then
        echo "md"
      else
        echo "json"
      fi
      ;;
    *) echo "json" ;;
  esac
}


# JSON Response Building

json_ok() {
  local data="$1"
  local summary="${2:-}"
  local breadcrumbs="${3:-[]}"
  local context="${4:-"{}"}"
  local meta="${5:-"{}"}"

  jq -n \
    --argjson data "$data" \
    --arg summary "$summary" \
    --argjson breadcrumbs "$breadcrumbs" \
    --argjson context "$context" \
    --argjson meta "$meta" \
    '{
      ok: true,
      data: $data,
      summary: $summary,
      breadcrumbs: $breadcrumbs,
      context: $context,
      meta: $meta
    }'
}

json_error() {
  local message="$1"
  local code="${2:-error}"
  local hint="${3:-}"

  if [[ -n "$hint" ]]; then
    jq -n \
      --arg message "$message" \
      --arg code "$code" \
      --arg hint "$hint" \
      '{ok: false, error: $message, code: $code, hint: $hint}'
  else
    jq -n \
      --arg message "$message" \
      --arg code "$code" \
      '{ok: false, error: $message, code: $code}'
  fi
}


# Breadcrumb Generation

breadcrumb() {
  local action="$1"
  local cmd="$2"
  local description="${3:-}"

  jq -n \
    --arg action "$action" \
    --arg cmd "$cmd" \
    --arg description "$description" \
    '{action: $action, cmd: $cmd, description: $description}'
}

breadcrumbs() {
  local result="["
  local first=true
  for bc in "$@"; do
    if [[ "$first" == "true" ]]; then
      first=false
    else
      result+=","
    fi
    result+="$bc"
  done
  result+="]"
  echo "$result"
}


# Markdown Output

md_heading() {
  local level="${1:-2}"
  local text="$2"
  local prefix=""
  for ((i=0; i<level; i++)); do
    prefix+="#"
  done
  echo "$prefix $text"
  echo
}

md_table() {
  local data="$1"
  shift

  local header="|"
  local separator="|"
  local jq_fields=""
  local first=true

  for col in "$@"; do
    local name="${col%%:*}"
    local field="${col##*:}"
    header+=" $name |"
    separator+="---|"
    if [[ "$first" == "true" ]]; then
      first=false
      jq_fields+="\(.$field)"
    else
      jq_fields+=" | \(.$field)"
    fi
  done

  echo "$header"
  echo "$separator"
  echo "$data" | jq -r ".[] | \"| $jq_fields |\""
}

md_kv() {
  echo "| Field | Value |"
  echo "|-------|-------|"
  while [[ $# -ge 2 ]]; do
    echo "| **$1** | $2 |"
    shift 2
  done
  echo
}

md_breadcrumbs() {
  local breadcrumbs="$1"
  local count
  count=$(echo "$breadcrumbs" | jq 'length')

  if [[ "$count" -gt 0 ]]; then
    echo "### Actions"
    echo "$breadcrumbs" | jq -r '.[] | "- `\(.cmd)` — \(.description)"'
    echo
  fi
}


# Output Dispatch

output() {
  local data="$1"
  local summary="${2:-}"
  local breadcrumbs="${3:-[]}"
  local md_renderer="${4:-}"
  local context="${5:-{}}"
  local meta="${6:-{}}"

  if [[ "$BCQ_QUIET" == "true" ]]; then
    echo "$data"
    return
  fi

  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    json_ok "$data" "$summary" "$breadcrumbs" "$context" "$meta"
  else
    if [[ -n "$md_renderer" ]] && declare -f "$md_renderer" > /dev/null; then
      "$md_renderer" "$data" "$summary" "$breadcrumbs" "$context" "$meta"
    else
      if [[ -n "$summary" ]]; then
        echo "$summary"
        echo
      fi
      echo '```json'
      echo "$data" | jq .
      echo '```'
      echo
      md_breadcrumbs "$breadcrumbs"
    fi
  fi
}

output_error() {
  local message="$1"
  local code="${2:-error}"
  local hint="${3:-}"

  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    json_error "$message" "$code" "$hint" >&2
  else
    echo "✗ $message" >&2
    if [[ -n "$hint" ]]; then
      echo >&2
      echo "$hint" >&2
    fi
  fi
}


# Exit Codes

EXIT_OK=0
EXIT_USAGE=1
EXIT_NOT_FOUND=2
EXIT_AUTH=3
EXIT_FORBIDDEN=4
EXIT_RATE_LIMIT=5
EXIT_NETWORK=6
EXIT_API=7
EXIT_AMBIGUOUS=8

die() {
  local message="$1"
  local code="${2:-1}"
  local hint="${3:-}"

  local error_code="error"
  case "$code" in
    $EXIT_USAGE) error_code="usage" ;;
    $EXIT_NOT_FOUND) error_code="not_found" ;;
    $EXIT_AUTH) error_code="auth_required" ;;
    $EXIT_FORBIDDEN) error_code="forbidden" ;;
    $EXIT_RATE_LIMIT) error_code="rate_limit" ;;
    $EXIT_NETWORK) error_code="network" ;;
    $EXIT_API) error_code="api_error" ;;
    $EXIT_AMBIGUOUS) error_code="ambiguous" ;;
  esac

  output_error "$message" "$error_code" "$hint"
  exit "$code"
}


# Global Flag Parsing

parse_global_flags() {
  GLOBAL_FLAGS_CONSUMED=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --json|-j)
        BCQ_FORMAT="json"
        (( ++GLOBAL_FLAGS_CONSUMED ))
        shift
        ;;
      --md|-m|--markdown)
        BCQ_FORMAT="md"
        (( ++GLOBAL_FLAGS_CONSUMED ))
        shift
        ;;
      --quiet|-q|--data)
        BCQ_QUIET="true"
        (( ++GLOBAL_FLAGS_CONSUMED ))
        shift
        ;;
      --verbose|-v)
        BCQ_VERBOSE="true"
        (( ++GLOBAL_FLAGS_CONSUMED ))
        shift
        ;;
      --project|-p)
        if [[ -z "${2:-}" ]]; then
          die "--project requires a value" $EXIT_USAGE
        fi
        BCQ_PROJECT="$2"
        (( GLOBAL_FLAGS_CONSUMED += 2 ))
        shift 2
        ;;
      --account|-a)
        if [[ -z "${2:-}" ]]; then
          die "--account requires a value" $EXIT_USAGE
        fi
        BCQ_ACCOUNT="$2"
        (( GLOBAL_FLAGS_CONSUMED += 2 ))
        shift 2
        ;;
      --)
        (( ++GLOBAL_FLAGS_CONSUMED ))
        break
        ;;
      -*)
        break
        ;;
      *)
        break
        ;;
    esac
  done
}


# Logging

debug() {
  if [[ "$BCQ_VERBOSE" == "true" ]]; then
    echo "[debug] $*" >&2
  fi
}

info() {
  if [[ "$BCQ_QUIET" != "true" ]]; then
    echo "$*" >&2
  fi
}

warn() {
  echo "⚠ $*" >&2
}


# Utilities

has_command() {
  command -v "$1" &> /dev/null
}

require_command() {
  local cmd="$1"
  local hint="${2:-Please install $cmd}"
  if ! has_command "$cmd"; then
    die "Required command not found: $cmd" $EXIT_USAGE "$hint"
  fi
}

urlencode() {
  local string="$1"
  python3 -c "import urllib.parse; print(urllib.parse.quote('''$string''', safe=''))"
}
