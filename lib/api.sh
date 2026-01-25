#!/usr/bin/env bash
# api.sh - HTTP helpers for Basecamp API
# Handles authentication, rate limiting, pagination


# Configuration
# BCQ_API_URL is set in core.sh - used for API resource access
# BCQ_BASE_URL is used for OAuth (separate in development)

BCQ_USER_AGENT="bcq/$BCQ_VERSION (https://github.com/basecamp/bcq)"
BCQ_MAX_RETRIES="${BCQ_MAX_RETRIES:-5}"
BCQ_BASE_DELAY="${BCQ_BASE_DELAY:-1}"
BCQ_CACHE_TTL="${BCQ_CACHE_TTL:-86400}"  # 24 hours default
BCQ_CONNECT_TIMEOUT="${BCQ_CONNECT_TIMEOUT:-10}"  # Connection timeout in seconds
BCQ_REQUEST_TIMEOUT="${BCQ_REQUEST_TIMEOUT:-30}"  # Total request timeout in seconds


# ETag Cache Helpers

_cache_dir() {
  # Check layered config first (includes --cache-dir flag and env var)
  local configured
  configured=$(get_config "cache_dir" "")
  if [[ -n "$configured" ]]; then
    echo "$configured"
  else
    echo "${XDG_CACHE_HOME:-$HOME/.cache}/bcq"
  fi
}

_cache_key() {
  local account_id="$1" url="$2" token="$3"
  # Include origin (API URL), account, path, and token hash for isolation
  # Token hash ensures different auth contexts don't share cache
  local token_hash=""
  if [[ -n "$token" ]]; then
    if command -v shasum &>/dev/null; then
      token_hash=$(echo -n "$token" | shasum -a 256 | cut -c1-16)
    else
      token_hash=$(echo -n "$token" | sha256sum | cut -c1-16)
    fi
  fi
  local cache_input="${BCQ_API_URL:-}:${account_id}:${token_hash}:${url}"
  # Use shasum (macOS) or sha256sum (Linux) for portability
  if command -v shasum &>/dev/null; then
    echo -n "$cache_input" | shasum -a 256 | cut -d' ' -f1
  else
    echo -n "$cache_input" | sha256sum | cut -d' ' -f1
  fi
}

_cache_get_etag() {
  local key="$1"
  local etags_file="$(_cache_dir)/etags.json"
  if [[ -f "$etags_file" ]]; then
    jq -r --arg k "$key" '.[$k].etag // .[$k] // empty' "$etags_file" 2>/dev/null || true
  fi
}

_cache_get_timestamp() {
  local key="$1"
  local etags_file="$(_cache_dir)/etags.json"
  if [[ -f "$etags_file" ]]; then
    jq -r --arg k "$key" '.[$k].timestamp // 0' "$etags_file" 2>/dev/null || echo "0"
  else
    echo "0"
  fi
}

_cache_is_expired() {
  local key="$1"
  local ttl="${2:-$BCQ_CACHE_TTL}"
  local timestamp
  timestamp=$(_cache_get_timestamp "$key")
  local now
  now=$(date +%s)
  (( now - timestamp > ttl ))
}

_cache_get_body() {
  local key="$1"
  local body_file="$(_cache_dir)/responses/${key}.body"
  if [[ -f "$body_file" ]]; then
    cat "$body_file"
  fi
}

_cache_get_headers() {
  local key="$1"
  local headers_file="$(_cache_dir)/responses/${key}.headers"
  if [[ -f "$headers_file" ]]; then
    cat "$headers_file"
  fi
}

_cache_set() {
  local key="$1" body="$2" etag="$3" headers="$4"
  local cache_dir="$(_cache_dir)"
  local etags_file="$cache_dir/etags.json"
  local body_file="$cache_dir/responses/${key}.body"
  local headers_file="$cache_dir/responses/${key}.headers"
  local timestamp
  timestamp=$(date +%s)

  mkdir -p "$cache_dir/responses"

  # Atomic write for body
  echo "$body" > "${body_file}.tmp" && mv "${body_file}.tmp" "$body_file"

  # Atomic write for headers (if provided)
  if [[ -n "$headers" ]]; then
    echo "$headers" > "${headers_file}.tmp" && mv "${headers_file}.tmp" "$headers_file"
  fi

  # Update etags.json with etag and timestamp (create if missing)
  # Format: {key: {etag: "...", timestamp: 123456}}
  if [[ -f "$etags_file" ]]; then
    jq --arg k "$key" --arg v "$etag" --argjson ts "$timestamp" \
      '.[$k] = {etag: $v, timestamp: $ts}' "$etags_file" > "${etags_file}.tmp" \
      && mv "${etags_file}.tmp" "$etags_file"
  else
    jq -n --arg k "$key" --arg v "$etag" --argjson ts "$timestamp" \
      '{($k): {etag: $v, timestamp: $ts}}' > "$etags_file"
  fi
}

# Prune expired cache entries (call periodically to prevent unbounded growth)
_cache_prune() {
  local cache_dir etags_file
  cache_dir=$(_cache_dir)
  etags_file="$cache_dir/etags.json"

  [[ -f "$etags_file" ]] || return 0

  local now ttl
  now=$(date +%s)
  ttl="${BCQ_CACHE_TTL:-86400}"

  # Remove expired entries from etags.json
  local valid_keys
  valid_keys=$(jq -r --argjson now "$now" --argjson ttl "$ttl" \
    'to_entries | map(select(.value.timestamp > ($now - $ttl))) | from_entries' \
    "$etags_file" 2>/dev/null) || return 0

  echo "$valid_keys" > "${etags_file}.tmp" && mv "${etags_file}.tmp" "$etags_file"

  # Get list of valid cache keys
  local valid_key_list
  valid_key_list=$(echo "$valid_keys" | jq -r 'keys[]' 2>/dev/null)

  # Remove orphaned response files (bodies are in responses/ subdir)
  local responses_dir="$cache_dir/responses"
  [[ -d "$responses_dir" ]] || return 0

  local file key
  for file in "$responses_dir"/*; do
    [[ -f "$file" ]] || continue
    # Extract key from filename (remove .body or .headers suffix)
    key=$(basename "$file" | sed 's/\.\(body\|headers\)$//')
    if ! echo "$valid_key_list" | grep -qx "$key"; then
      rm -f "$file"
      debug "Cache: pruned orphaned file $(basename "$file")"
    fi
  done
}


# Authentication

ensure_auth() {
  local token
  token=$(get_access_token) || {
    die "Not authenticated. Run: bcq auth login" $EXIT_AUTH
  }

  if is_token_expired && [[ -z "${BASECAMP_ACCESS_TOKEN:-}" ]]; then
    debug "Token expired, refreshing..."
    if ! refresh_token; then
      die "Token expired and refresh failed. Run: bcq auth login" $EXIT_AUTH
    fi
    token=$(get_access_token)
  fi

  echo "$token"
}

ensure_account_id() {
  local account_id
  account_id=$(get_account_id)
  if [[ -z "$account_id" ]]; then
    die "No account configured. Run: bcq config set account_id <id>" $EXIT_USAGE \
      "Or set BASECAMP_ACCOUNT_ID environment variable"
  fi
  echo "$account_id"
}


# HTTP Request Helpers

api_get() {
  local path="$1"
  shift

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local url="$BCQ_API_URL/$account_id$path"
  _api_request GET "$url" "$token" "" "$@"
}

api_post() {
  local path="$1"
  local body="${2:-}"
  shift 2 || shift

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local url="$BCQ_API_URL/$account_id$path"
  _api_request POST "$url" "$token" "$body" "$@"
}

api_put() {
  local path="$1"
  local body="${2:-}"
  shift 2 || shift

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local url="$BCQ_API_URL/$account_id$path"
  _api_request PUT "$url" "$token" "$body" "$@"
}

api_delete() {
  local path="$1"
  shift

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local url="$BCQ_API_URL/$account_id$path"
  _api_request DELETE "$url" "$token" "" "$@"
}

# Binary file upload (for attachments)
api_upload() {
  local path="$1"
  local file="$2"
  local content_type="${3:-application/octet-stream}"
  shift 3 || shift 2 || shift

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local url="$BCQ_API_URL/$account_id$path"
  local file_size
  file_size=$(wc -c < "$file" | tr -d ' ')

  debug "API UPLOAD $url ($content_type, $file_size bytes)"

  local headers_file response http_code
  headers_file=$(mktemp)
  trap "rm -f '$headers_file'" RETURN

  local output curl_exit
  output=$(curl -s \
    -X POST \
    -H "Authorization: Bearer $token" \
    -H "User-Agent: $BCQ_USER_AGENT" \
    -H "Content-Type: $content_type" \
    -H "Content-Length: $file_size" \
    --data-binary "@$file" \
    -D "$headers_file" \
    -w '\n%{http_code}' \
    "$url") || curl_exit=$?

  if [[ -n "${curl_exit:-}" ]]; then
    case "$curl_exit" in
      6)  die "Could not resolve host" $EXIT_NETWORK ;;
      7)  die "Connection refused" $EXIT_NETWORK ;;
      28) die "Connection timed out" $EXIT_NETWORK ;;
      *)  die "Network error (curl exit $curl_exit)" $EXIT_NETWORK ;;
    esac
  fi

  http_code=$(echo "$output" | tail -n1)
  response=$(echo "$output" | sed '$d')

  debug "HTTP $http_code"

  case "$http_code" in
    200|201)
      echo "$response"
      return 0
      ;;
    401)
      die "Authentication failed" $EXIT_AUTH "Run: bcq auth login"
      ;;
    403)
      die "Permission denied" $EXIT_FORBIDDEN
      ;;
    404)
      die "Not found" $EXIT_NOT_FOUND
      ;;
    *)
      local error_msg
      error_msg=$(echo "$response" | jq -r '.error // .message // "Upload failed"' 2>/dev/null || echo "Upload failed")
      die "$error_msg (HTTP $http_code)" $EXIT_API
      ;;
  esac
}

_api_request() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"
  shift 4 || shift 3 || shift 2 || shift

  local attempt=1
  local delay=$BCQ_BASE_DELAY
  local response http_code headers_file

  headers_file=$(mktemp)
  trap "rm -f '$headers_file'" RETURN

  # ETag cache setup (GET requests only)
  local cache_key="" cached_etag=""
  if [[ "$method" == "GET" ]] && [[ "${BCQ_CACHE_ENABLED:-true}" == "true" ]]; then
    local account_id
    account_id=$(get_account_id 2>/dev/null || echo "")
    if [[ -n "$account_id" ]]; then
      cache_key=$(_cache_key "$account_id" "$url" "$token")
      # Only use cached ETag if cache hasn't expired
      if ! _cache_is_expired "$cache_key"; then
        cached_etag=$(_cache_get_etag "$cache_key")
      else
        debug "Cache: entry expired, forcing fresh request"
      fi
    fi
  fi

  while (( attempt <= BCQ_MAX_RETRIES )); do
    debug "API $method $url (attempt $attempt)"

    local curl_args=(
      -s
      -X "$method"
      --connect-timeout "$BCQ_CONNECT_TIMEOUT"
      --max-time "$BCQ_REQUEST_TIMEOUT"
      -H "Authorization: Bearer $token"
      -H "User-Agent: $BCQ_USER_AGENT"
      -H "Content-Type: application/json"
      -D "$headers_file"
      -w '\n%{http_code}'
    )

    # Add If-None-Match header for cached responses
    if [[ -n "$cached_etag" ]]; then
      curl_args+=(-H "If-None-Match: $cached_etag")
      debug "Cache: If-None-Match $cached_etag"
    fi

    if [[ -n "$body" ]]; then
      curl_args+=(-d "$body")
    fi

    curl_args+=("$@")
    curl_args+=("$url")

    # Log curl command in verbose mode (with redacted token)
    if [[ "$BCQ_VERBOSE" == "true" ]]; then
      local curl_cmd="curl"
      local prev_was_H=false
      for arg in "${curl_args[@]}"; do
        if [[ "$arg" == "-H" ]]; then
          prev_was_H=true
          continue
        elif $prev_was_H; then
          prev_was_H=false
          if [[ "$arg" == "Authorization: Bearer"* ]]; then
            curl_cmd+=" -H 'Authorization: Bearer [REDACTED]'"
          else
            curl_cmd+=" -H '$arg'"
          fi
        elif [[ "$arg" == *" "* ]]; then
          curl_cmd+=" '$arg'"
        else
          curl_cmd+=" $arg"
        fi
      done
      echo "[curl] $curl_cmd" >&2
    fi

    local output curl_exit
    output=$(curl "${curl_args[@]}") || curl_exit=$?

    if [[ -n "${curl_exit:-}" ]]; then
      case "$curl_exit" in
        6)  die "Could not resolve host" $EXIT_NETWORK ;;
        7)  die "Connection refused" $EXIT_NETWORK ;;
        28) die "Connection timed out" $EXIT_NETWORK ;;
        35) die "SSL/TLS handshake failed" $EXIT_NETWORK ;;
        *)  die "Network error (curl exit $curl_exit)" $EXIT_NETWORK ;;
      esac
    fi

    http_code=$(echo "$output" | tail -n1)
    response=$(echo "$output" | sed '$d')

    debug "HTTP $http_code"

    case "$http_code" in
      304)
        # Not Modified - return cached response
        if [[ -n "$cache_key" ]]; then
          debug "Cache hit: 304 Not Modified"
          _cache_get_body "$cache_key"
          return 0
        fi
        # Cache somehow missing, fall through to error
        die "304 received but no cached response available" $EXIT_API
        ;;
      200|201|204)
        # Cache successful GET responses with ETag
        if [[ "$method" == "GET" ]] && [[ -n "$cache_key" ]]; then
          local etag
          etag=$(grep -i '^ETag:' "$headers_file" | sed 's/^[^:]*:[[:space:]]*//' | tr -d '\r\n')
          if [[ -n "$etag" ]]; then
            local cached_headers
            cached_headers=$(cat "$headers_file")
            _cache_set "$cache_key" "$response" "$etag" "$cached_headers"
            debug "Cache: stored with ETag $etag"
          fi
        fi
        echo "$response"
        return 0
        ;;
      429)
        local retry_after
        retry_after=$(grep -i "Retry-After:" "$headers_file" | awk '{print $2}' | tr -d '\r')
        delay=${retry_after:-$((BCQ_BASE_DELAY * 2 ** (attempt - 1)))}
        info "Rate limited, waiting ${delay}s..."
        sleep "$delay"
        ((attempt++))
        ;;
      401)
        if [[ $attempt -eq 1 ]] && [[ -z "${BASECAMP_ACCESS_TOKEN:-}" ]]; then
          debug "401 received, attempting token refresh"
          if refresh_token; then
            token=$(get_access_token)
            ((attempt++))
            continue
          fi
        fi
        die "Authentication failed" $EXIT_AUTH "Run: bcq auth login"
        ;;
      403)
        # Check if this is likely a scope issue (write operation with read-only token)
        if [[ "$method" =~ ^(POST|PUT|PATCH|DELETE)$ ]]; then
          local current_scope
          current_scope=$(get_token_scope 2>/dev/null || echo "unknown")
          if [[ "$current_scope" == "read" ]]; then
            die "Permission denied: read-only token cannot perform write operations" $EXIT_FORBIDDEN \
              "Re-authenticate with full scope: bcq auth login --scope full"
          fi
        fi
        die "Permission denied" $EXIT_FORBIDDEN \
          "You don't have access to this resource"
        ;;
      404)
        die "Not found" $EXIT_NOT_FOUND
        ;;
      500)
        # Internal server error - don't retry, it's an application bug
        die "Server error (500)" $EXIT_API \
          "The server encountered an internal error"
        ;;
      502|503|504)
        # Gateway errors - transient, retry with backoff
        delay=$((BCQ_BASE_DELAY * 2 ** (attempt - 1)))
        info "Gateway error ($http_code), retrying in ${delay}s..."
        sleep "$delay"
        ((attempt++))
        ;;
      *)
        local error_msg
        error_msg=$(echo "$response" | jq -r '.error // .message // "Unknown error"' 2>/dev/null || echo "Request failed")
        die "$error_msg (HTTP $http_code)" $EXIT_API
        ;;
    esac
  done

  die "Request failed after $BCQ_MAX_RETRIES retries" $EXIT_API
}


# Pagination

api_get_all() {
  local path="$1"
  local max_pages="${2:-100}"

  local token account_id
  token=$(ensure_auth)
  account_id=$(ensure_account_id)

  local all_results="[]"
  local page=1
  local url="$BCQ_API_URL/$account_id$path"

  [[ "$url" != *.json ]] && url="$url.json"

  local headers_file
  headers_file=$(mktemp)
  trap "rm -f '$headers_file'" RETURN

  local page_attempt=0

  while (( page <= max_pages )); do
    debug "Fetching page $page: $url"

    local output http_code response curl_exit
    output=$(curl -s \
      --connect-timeout "$BCQ_CONNECT_TIMEOUT" \
      --max-time "$BCQ_REQUEST_TIMEOUT" \
      -H "Authorization: Bearer $token" \
      -H "User-Agent: $BCQ_USER_AGENT" \
      -H "Content-Type: application/json" \
      -D "$headers_file" \
      -w '\n%{http_code}' \
      "$url") || curl_exit=$?

    if [[ -n "${curl_exit:-}" ]]; then
      case "$curl_exit" in
        6)  die "Could not resolve host" $EXIT_NETWORK ;;
        7)  die "Connection refused" $EXIT_NETWORK ;;
        28) die "Connection timed out" $EXIT_NETWORK ;;
        35) die "SSL/TLS handshake failed" $EXIT_NETWORK ;;
        *)  die "Network error (curl exit $curl_exit)" $EXIT_NETWORK ;;
      esac
    fi

    http_code=$(echo "$output" | tail -n1)
    response=$(echo "$output" | sed '$d')

    debug "HTTP $http_code (page $page)"

    case "$http_code" in
      200)
        # Success - reset attempt counter for next page
        page_attempt=0
        ;;
      429)
        # Rate limited - wait and retry with cap
        ((page_attempt++))
        if (( page_attempt > BCQ_MAX_RETRIES )); then
          die "Rate limited after $BCQ_MAX_RETRIES retries" $EXIT_API
        fi
        local retry_after
        retry_after=$(grep -i "Retry-After:" "$headers_file" | awk '{print $2}' | tr -d '\r')
        local delay=${retry_after:-2}
        info "Rate limited, waiting ${delay}s (attempt $page_attempt/$BCQ_MAX_RETRIES)..."
        sleep "$delay"
        continue  # Retry same page
        ;;
      401)
        # Auth failed - try to refresh token
        ((page_attempt++))
        if (( page_attempt > BCQ_MAX_RETRIES )); then
          die "Authentication failed after $BCQ_MAX_RETRIES attempts. Run: bcq auth login" $EXIT_AUTH
        fi
        if [[ -z "${BASECAMP_ACCESS_TOKEN:-}" ]]; then
          debug "401 received during pagination, attempting token refresh"
          if refresh_token; then
            token=$(ensure_auth)
            continue  # Retry same page with new token
          fi
        fi
        die "Authentication failed (HTTP 401). Run: bcq auth login" $EXIT_AUTH
        ;;
      403)
        die "Permission denied" $EXIT_FORBIDDEN \
          "You don't have access to this resource"
        ;;
      404)
        die "Not found" $EXIT_NOT_FOUND
        ;;
      500)
        die "Server error (500)" $EXIT_API \
          "The server encountered an internal error"
        ;;
      502|503|504)
        # Gateway errors - transient, retry with backoff
        ((page_attempt++))
        if (( page_attempt > BCQ_MAX_RETRIES )); then
          die "Gateway error after $BCQ_MAX_RETRIES retries" $EXIT_API
        fi
        local delay=$((BCQ_BASE_DELAY * 2 ** (page_attempt - 1)))
        info "Gateway error ($http_code), retrying in ${delay}s..."
        sleep "$delay"
        continue  # Retry same page
        ;;
      *)
        local error_msg
        error_msg=$(echo "$response" | jq -r '.error // .message // "Unknown error"' 2>/dev/null || echo "Request failed")
        die "$error_msg (HTTP $http_code)" $EXIT_API
        ;;
    esac

    if [[ "$all_results" == "[]" ]]; then
      all_results="$response"
    else
      all_results=$(echo "$all_results" "$response" | jq -s '.[0] + .[1]')
    fi

    # Parse Link header for next page (RFC 5988)
    local next_url
    next_url=$(grep -i '^Link:' "$headers_file" | sed -n 's/.*<\([^>]*\)>; rel="next".*/\1/p' | tr -d '\r')

    if [[ -z "$next_url" ]]; then
      break
    fi

    url="$next_url"
    ((page++))
  done

  echo "$all_results"
}


# Token Refresh

refresh_token() {
  local creds
  creds=$(load_credentials)

  local refresh_tok oauth_type scope stored_token_endpoint
  refresh_tok=$(echo "$creds" | jq -r '.refresh_token // empty')
  oauth_type=$(echo "$creds" | jq -r '.oauth_type // "bc3"')
  scope=$(echo "$creds" | jq -r '.scope // empty')
  stored_token_endpoint=$(echo "$creds" | jq -r '.token_endpoint // empty')

  if [[ -z "$refresh_tok" ]]; then
    debug "No refresh token available"
    return 1
  fi

  debug "Refreshing token (oauth_type=$oauth_type)..."

  local client_id client_secret
  if [[ "$oauth_type" == "launchpad" ]]; then
    # Launchpad: Load client credentials from env/config
    if [[ -n "$BCQ_CLIENT_ID" ]] && [[ -n "$BCQ_CLIENT_SECRET" ]]; then
      client_id="$BCQ_CLIENT_ID"
      client_secret="$BCQ_CLIENT_SECRET"
    else
      client_id=$(get_config "oauth_client_id" "")
      client_secret=$(get_config "oauth_client_secret" "")
    fi

    if [[ -z "$client_id" ]] || [[ -z "$client_secret" ]]; then
      debug "No Launchpad client credentials found"
      return 1
    fi
  else
    # BC3: Load client credentials from file
    local client_file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CLIENT_FILE"
    if [[ ! -f "$client_file" ]]; then
      debug "No client credentials found"
      return 1
    fi

    client_id=$(jq -r '.client_id' "$client_file")
    client_secret=$(jq -r '.client_secret // ""' "$client_file")
  fi

  local token_endpoint response curl_exit

  # Use stored token endpoint if available (preferred - avoids discovery dependency)
  # Fall back to discovery/defaults for legacy credentials without stored endpoint
  if [[ -n "$stored_token_endpoint" ]]; then
    token_endpoint="$stored_token_endpoint"
    debug "Using stored token endpoint: $token_endpoint"
  elif [[ "$oauth_type" == "launchpad" ]]; then
    token_endpoint="$BCQ_LAUNCHPAD_TOKEN_URL"
  else
    # Legacy BC3 credentials without stored endpoint - must use discovery
    token_endpoint=$(_token_endpoint)
  fi

  if [[ "$oauth_type" == "launchpad" ]]; then
    # Launchpad OAuth 2 refresh (uses type=refresh, not grant_type)
    response=$(curl -s -X POST \
      -H "Content-Type: application/x-www-form-urlencoded" \
      --data-urlencode "type=refresh" \
      --data-urlencode "refresh_token=$refresh_tok" \
      --data-urlencode "client_id=$client_id" \
      --data-urlencode "client_secret=$client_secret" \
      "$token_endpoint") || curl_exit=$?
  else
    # BC3 OAuth 2.1 refresh (standard RFC 6749)
    local curl_args=(
      -s -X POST
      -H "Content-Type: application/x-www-form-urlencoded"
      --data-urlencode "grant_type=refresh_token"
      --data-urlencode "refresh_token=$refresh_tok"
      --data-urlencode "client_id=$client_id"
    )

    # Only include client_secret for confidential clients
    if [[ -n "$client_secret" ]]; then
      curl_args+=(--data-urlencode "client_secret=$client_secret")
    fi

    response=$(curl "${curl_args[@]}" "$token_endpoint") || curl_exit=$?
  fi

  if [[ -n "${curl_exit:-}" ]]; then
    debug "Token refresh network error (curl exit $curl_exit)"
    return 1
  fi

  local new_access_token new_refresh_token expires_in
  new_access_token=$(echo "$response" | jq -r '.access_token // empty')
  new_refresh_token=$(echo "$response" | jq -r '.refresh_token // empty')
  expires_in=$(echo "$response" | jq -r '.expires_in // 7200')

  if [[ -z "$new_access_token" ]]; then
    debug "Token refresh failed: $response"
    return 1
  fi

  local expires_at
  expires_at=$(($(date +%s) + expires_in))

  # Preserve oauth_type, scope, and token_endpoint in refreshed credentials
  local new_creds
  new_creds=$(jq -n \
    --arg access_token "$new_access_token" \
    --arg refresh_token "${new_refresh_token:-$refresh_tok}" \
    --argjson expires_at "$expires_at" \
    --arg scope "$scope" \
    --arg oauth_type "$oauth_type" \
    --arg token_endpoint "$token_endpoint" \
    '{
      access_token: $access_token,
      refresh_token: $refresh_token,
      expires_at: $expires_at,
      scope: $scope,
      oauth_type: $oauth_type,
      token_endpoint: $token_endpoint
    }')

  save_credentials "$new_creds"
  debug "Token refreshed successfully"
  return 0
}


# URL Building

project_path() {
  local resource="$1"
  local project_id="${2:-$(get_project_id)}"

  if [[ -z "$project_id" ]]; then
    die "No project specified. Use --project or set in .basecamp/config.json" $EXIT_USAGE
  fi

  echo "/buckets/$project_id$resource"
}


# Prune cache on startup (fast no-op if nothing to prune)
_cache_prune 2>/dev/null &
