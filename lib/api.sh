#!/usr/bin/env bash
# api.sh - HTTP helpers for Basecamp API
# Handles authentication, rate limiting, pagination


# Configuration
# BCQ_API_URL is set in core.sh - used for API resource access
# BCQ_BASE_URL is used for OAuth (separate in development)

BCQ_USER_AGENT="bcq/$BCQ_VERSION (https://github.com/basecamp/bcq)"
BCQ_MAX_RETRIES="${BCQ_MAX_RETRIES:-5}"
BCQ_BASE_DELAY="${BCQ_BASE_DELAY:-1}"


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

  while (( attempt <= BCQ_MAX_RETRIES )); do
    debug "API $method $url (attempt $attempt)"

    local curl_args=(
      -s
      -X "$method"
      -H "Authorization: Bearer $token"
      -H "User-Agent: $BCQ_USER_AGENT"
      -H "Content-Type: application/json"
      -D "$headers_file"
      -w '\n%{http_code}'
    )

    if [[ -n "$body" ]]; then
      curl_args+=(-d "$body")
    fi

    curl_args+=("$@")
    curl_args+=("$url")

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
      200|201|204)
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

  while (( page <= max_pages )); do
    debug "Fetching page $page: $url"

    local output http_code response curl_exit
    output=$(curl -s \
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
        *)  die "Network error (curl exit $curl_exit)" $EXIT_NETWORK ;;
      esac
    fi

    http_code=$(echo "$output" | tail -n1)
    response=$(echo "$output" | sed '$d')

    if [[ "$http_code" != "200" ]]; then
      die "API request failed (HTTP $http_code)" $EXIT_API
    fi

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

  local refresh_token
  refresh_token=$(echo "$creds" | jq -r '.refresh_token // empty')

  if [[ -z "$refresh_token" ]]; then
    debug "No refresh token available"
    return 1
  fi

  local client_file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CLIENT_FILE"
  if [[ ! -f "$client_file" ]]; then
    debug "No client credentials found"
    return 1
  fi

  local client_id client_secret
  client_id=$(jq -r '.client_id' "$client_file")
  client_secret=$(jq -r '.client_secret' "$client_file")

  debug "Refreshing token..."

  local token_endpoint
  token_endpoint=$(_token_endpoint)

  local response curl_exit
  response=$(curl -s -X POST \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=refresh_token" \
    -d "refresh_token=$refresh_token" \
    -d "client_id=$client_id" \
    -d "client_secret=$client_secret" \
    "$token_endpoint") || curl_exit=$?

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

  local new_creds
  new_creds=$(jq -n \
    --arg access_token "$new_access_token" \
    --arg refresh_token "${new_refresh_token:-$refresh_token}" \
    --argjson expires_at "$expires_at" \
    '{access_token: $access_token, refresh_token: $refresh_token, expires_at: $expires_at}')

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
