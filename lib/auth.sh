#!/usr/bin/env bash
# auth.sh - OAuth authentication with support for multiple providers
#
# Supports two OAuth providers:
# 1. Basecamp OAuth 2.1 (BC3) - Uses DCR, PKCE, .well-known discovery
# 2. Launchpad OAuth 2 - Pre-registered clients, no PKCE
#
# Strategy: Try BC3 OAuth 2.1 discovery first, fall back to Launchpad if unavailable


# OAuth Configuration

BCQ_REDIRECT_PORT="${BCQ_REDIRECT_PORT:-8976}"
BCQ_REDIRECT_URI="http://127.0.0.1:$BCQ_REDIRECT_PORT/callback"

BCQ_CLIENT_NAME="bcq"
BCQ_CLIENT_URI="https://github.com/basecamp/bcq"

# Configurable client credentials for Launchpad OAuth (no DCR support)
# Register at https://integrate.37signals.com to get client_id/client_secret
BCQ_CLIENT_ID="${BCQ_CLIENT_ID:-}"
BCQ_CLIENT_SECRET="${BCQ_CLIENT_SECRET:-}"

# Launchpad OAuth 2 endpoints (configurable for dev/staging/production)
# Production: https://launchpad.37signals.com
# Staging:    https://launchpad-staging.37signals.com
# Dev:        http://launchpad.localhost:3011
BCQ_LAUNCHPAD_URL="${BCQ_LAUNCHPAD_URL:-https://launchpad.37signals.com}"
BCQ_LAUNCHPAD_AUTH_URL="${BCQ_LAUNCHPAD_AUTH_URL:-$BCQ_LAUNCHPAD_URL/authorization/new}"
BCQ_LAUNCHPAD_TOKEN_URL="${BCQ_LAUNCHPAD_TOKEN_URL:-$BCQ_LAUNCHPAD_URL/authorization/token}"

# Cached OAuth server metadata (populated by _ensure_oauth_config)
declare -g _BCQ_OAUTH_CONFIG=""
declare -g _BCQ_OAUTH_TYPE=""  # "bc3" or "launchpad"


# OAuth Discovery

_discover_oauth_config() {
  # Try BC3 OAuth 2.1 discovery first (RFC 8414)
  local discovery_url="$BCQ_BASE_URL/.well-known/oauth-authorization-server"

  debug "Attempting BC3 OAuth 2.1 discovery from: $discovery_url"

  local response
  if response=$(curl -s -f "$discovery_url" 2>/dev/null); then
    # Validate required fields
    local authorization_endpoint token_endpoint
    authorization_endpoint=$(echo "$response" | jq -r '.authorization_endpoint // empty')
    token_endpoint=$(echo "$response" | jq -r '.token_endpoint // empty')

    if [[ -n "$authorization_endpoint" ]] && [[ -n "$token_endpoint" ]]; then
      _BCQ_OAUTH_CONFIG="$response"
      _BCQ_OAUTH_TYPE="bc3"
      debug "BC3 OAuth 2.1 config discovered successfully"
      return 0
    fi
  fi

  # Fall back to Launchpad OAuth 2
  debug "BC3 OAuth 2.1 discovery failed, using Launchpad OAuth 2"
  _BCQ_OAUTH_TYPE="launchpad"

  # Synthesize a config object for Launchpad
  _BCQ_OAUTH_CONFIG=$(jq -n \
    --arg auth "$BCQ_LAUNCHPAD_AUTH_URL" \
    --arg token "$BCQ_LAUNCHPAD_TOKEN_URL" \
    '{
      authorization_endpoint: $auth,
      token_endpoint: $token,
      grant_types_supported: ["authorization_code", "refresh_token"]
    }')

  debug "Launchpad OAuth 2 config configured"
}

_ensure_oauth_config() {
  # Lazily fetch and cache OAuth config
  if [[ -z "$_BCQ_OAUTH_CONFIG" ]]; then
    _discover_oauth_config
  fi
}

_get_oauth_endpoint() {
  local key="$1"
  _ensure_oauth_config
  echo "$_BCQ_OAUTH_CONFIG" | jq -r ".$key // empty"
}

_get_oauth_type() {
  _ensure_oauth_config
  echo "$_BCQ_OAUTH_TYPE"
}

# Convenience accessors for OAuth endpoints
_authorization_endpoint() { _get_oauth_endpoint "authorization_endpoint"; }
_token_endpoint() { _get_oauth_endpoint "token_endpoint"; }
_registration_endpoint() { _get_oauth_endpoint "registration_endpoint"; }
_introspection_endpoint() { _get_oauth_endpoint "introspection_endpoint"; }


# Auth Commands

cmd_auth() {
  local action="${1:-status}"
  shift || true

  case "$action" in
    login) _auth_login "$@" ;;
    logout) _auth_logout "$@" ;;
    status) _auth_status "$@" ;;
    refresh) _auth_refresh "$@" ;;
    --help|-h) _help_auth ;;
    *)
      die "Unknown auth action: $action" $EXIT_USAGE "Run: bcq auth --help"
      ;;
  esac
}


# Login Flow

_auth_login() {
  local no_browser=false
  local scope="full"  # Default to full (read+write) scope

  # Parse login-specific flags
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --no-browser)
        no_browser=true
        shift
        ;;
      --scope)
        shift
        case "${1:-}" in
          full|read)
            scope="$1"
            shift
            ;;
          *)
            die "Invalid scope: ${1:-}. Use 'full' or 'read'" $EXIT_USAGE
            ;;
        esac
        ;;
      *)
        shift
        ;;
    esac
  done

  info "Starting authentication..."

  # Check for existing valid token
  if get_access_token &>/dev/null && ! is_token_expired; then
    info "Already authenticated. Use 'bcq auth logout' first to re-authenticate."
    _auth_status
    return 0
  fi

  # Pre-fetch OAuth config (discovers provider type: bc3 or launchpad)
  _ensure_oauth_config

  local oauth_type
  oauth_type=$(_get_oauth_type)
  debug "Using OAuth type: $oauth_type"

  # Warn about scope limitations for Launchpad
  if [[ "$oauth_type" == "launchpad" ]] && [[ "$scope" == "read" ]]; then
    warn "Launchpad OAuth does not support read-only scope; granting full access"
    scope="full"
  fi

  # Get or register client based on OAuth type
  local client_id client_secret
  if [[ "$oauth_type" == "bc3" ]]; then
    # BC3: Use Dynamic Client Registration
    if ! _load_client; then
      info "Registering OAuth client..."
      _register_client || die "Failed to register OAuth client" $EXIT_AUTH
    fi
    _load_client
  else
    # Launchpad: Use pre-configured client credentials
    _load_launchpad_client || die "Launchpad OAuth requires client credentials" $EXIT_AUTH \
      "Set BCQ_CLIENT_ID and BCQ_CLIENT_SECRET environment variables" \
      "Register at https://integrate.37signals.com"
  fi

  # Generate state for CSRF protection
  local state
  state=$(_generate_state)

  # Build authorization URL based on OAuth type
  local auth_endpoint auth_url
  auth_endpoint=$(_authorization_endpoint)

  if [[ "$oauth_type" == "bc3" ]]; then
    # BC3 OAuth 2.1 with PKCE
    local code_verifier code_challenge
    code_verifier=$(_generate_code_verifier)
    code_challenge=$(_generate_code_challenge "$code_verifier")
    debug "Generated code_verifier: $code_verifier"
    debug "Generated code_challenge: $code_challenge"

    auth_url="$auth_endpoint?response_type=code"
    auth_url+="&client_id=$client_id"
    auth_url+="&redirect_uri=$(urlencode "$BCQ_REDIRECT_URI")"
    auth_url+="&code_challenge=$code_challenge"
    auth_url+="&code_challenge_method=S256"
    auth_url+="&scope=$scope"
    auth_url+="&state=$state"
  else
    # Launchpad OAuth 2 (no PKCE)
    local code_verifier=""  # Not used for Launchpad
    auth_url="$auth_endpoint?type=web_server"
    auth_url+="&client_id=$client_id"
    auth_url+="&redirect_uri=$(urlencode "$BCQ_REDIRECT_URI")"
    auth_url+="&state=$state"
  fi

  local auth_code

  if [[ "$no_browser" == "true" ]]; then
    # Headless mode: user manually visits URL and enters code
    echo "Visit this URL to authorize:"
    echo
    echo "  $auth_url"
    echo
    read -rp "Enter the authorization code: " auth_code
    if [[ -z "$auth_code" ]]; then
      die "No authorization code provided" $EXIT_AUTH
    fi
  else
    # Browser mode: open browser and wait for callback
    info "Opening browser for authorization..."
    info "If browser doesn't open, visit: $auth_url"

    # Open browser
    _open_browser "$auth_url"

    # Start local server to receive callback
    auth_code=$(_wait_for_callback "$state") || die "Authorization failed" $EXIT_AUTH
  fi

  # Exchange code for tokens
  info "Exchanging authorization code..."
  _exchange_code "$auth_code" "$code_verifier" "$client_id" "$client_secret" "$oauth_type" || \
    die "Token exchange failed" $EXIT_AUTH

  # Discover accounts
  info "Discovering accounts..."
  _discover_accounts || warn "Could not discover accounts"

  # Select account if multiple
  _select_account

  info "Authentication successful!"
  _auth_status
}

_auth_logout() {
  if get_access_token &>/dev/null; then
    clear_credentials
    info "Logged out from $BCQ_BASE_URL"
  else
    info "Not logged in to $BCQ_BASE_URL"
  fi
}

_auth_status() {
  local format
  format=$(get_format)

  local auth_status="unauthenticated"
  local user_email=""
  local account_id=""
  local account_name=""
  local expires_at=""
  local token_status="none"
  local scope=""
  local oauth_type=""

  if get_access_token &>/dev/null; then
    auth_status="authenticated"
    token_status="valid"

    local creds
    creds=$(load_credentials)
    expires_at=$(echo "$creds" | jq -r '.expires_at // 0')
    scope=$(echo "$creds" | jq -r '.scope // empty')
    oauth_type=$(echo "$creds" | jq -r '.oauth_type // empty')

    if is_token_expired; then
      token_status="expired"
    fi

    local accounts
    accounts=$(load_accounts)
    account_id=$(get_account_id)

    if [[ -n "$account_id" ]] && [[ "$accounts" != "[]" ]]; then
      account_name=$(echo "$accounts" | jq -r --arg id "$account_id" \
        '.[] | select(.id == ($id | tonumber)) | .name // empty')
    fi
  fi

  if [[ "$format" == "json" ]]; then
    jq -n \
      --arg status "$auth_status" \
      --arg token_status "$token_status" \
      --arg account_id "$account_id" \
      --arg account_name "$account_name" \
      --arg expires_at "$expires_at" \
      --arg scope "$scope" \
      --arg oauth_type "$oauth_type" \
      '{
        status: $status,
        token: $token_status,
        scope: (if $scope != "" then $scope else null end),
        oauth_provider: (if $oauth_type != "" then $oauth_type else null end),
        account: {
          id: (if $account_id != "" then ($account_id | tonumber) else null end),
          name: (if $account_name != "" then $account_name else null end)
        },
        expires_at: (if $expires_at != "" and $expires_at != "0" then ($expires_at | tonumber) else null end)
      }'
  else
    echo "## Authentication Status"
    echo
    if [[ "$auth_status" == "authenticated" ]]; then
      echo "Status: ✓ Authenticated"
      [[ -n "$account_name" ]] && echo "Account: $account_name (#$account_id)" || true
      if [[ -n "$oauth_type" ]]; then
        if [[ "$oauth_type" == "launchpad" ]]; then
          echo "Provider: Launchpad OAuth 2"
        else
          echo "Provider: Basecamp OAuth 2.1"
        fi
      fi
      if [[ -n "$scope" ]]; then
        if [[ "$scope" == "read" ]]; then
          echo "Scope: $scope (read-only)"
        else
          echo "Scope: $scope (read+write)"
        fi
      fi
      [[ "$token_status" == "expired" ]] && echo "Token: ⚠ Expired (will refresh on next request)" || true
    else
      echo "Status: ✗ Not authenticated"
      echo
      echo "Run: bcq auth login"
    fi
  fi
}

_auth_refresh() {
  if refresh_token; then
    info "Token refreshed successfully"
    _auth_status
  else
    die "Token refresh failed. Run: bcq auth login" $EXIT_AUTH
  fi
}


# OAuth Helpers

_register_client() {
  # Dynamic Client Registration (DCR) using discovered endpoint
  local registration_endpoint
  registration_endpoint=$(_registration_endpoint)

  if [[ -z "$registration_endpoint" ]]; then
    die "OAuth server does not support Dynamic Client Registration" $EXIT_AUTH \
      "The server's .well-known/oauth-authorization-server does not include registration_endpoint"
  fi

  debug "Registering client at: $registration_endpoint"

  # DCR clients typically only get authorization_code grant
  # (refresh_token is often restricted to pre-registered clients)
  local grant_types='["authorization_code"]'

  # BC3 DCR only supports public clients (no client_secret)
  # Account discovery uses /authorization.json self-introspection endpoint
  local response
  response=$(curl -s -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n \
      --arg name "$BCQ_CLIENT_NAME" \
      --arg uri "$BCQ_CLIENT_URI" \
      --arg redirect "$BCQ_REDIRECT_URI" \
      --argjson grants "$grant_types" \
      '{
        client_name: $name,
        client_uri: $uri,
        redirect_uris: [$redirect],
        grant_types: $grants,
        response_types: ["code"],
        token_endpoint_auth_method: "none"
      }')" \
    "$registration_endpoint")

  local client_id client_secret
  client_id=$(echo "$response" | jq -r '.client_id // empty')
  client_secret=$(echo "$response" | jq -r '.client_secret // empty')

  if [[ -z "$client_id" ]]; then
    debug "DCR response: $response"
    return 1
  fi

  debug "Registered client_id: $client_id"

  ensure_global_config_dir
  # Public clients may not have a client_secret
  jq -n \
    --arg client_id "$client_id" \
    --arg client_secret "${client_secret:-}" \
    '{client_id: $client_id, client_secret: $client_secret}' \
    > "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CLIENT_FILE"

  chmod 600 "$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CLIENT_FILE"
}

_load_client() {
  # Load BC3 DCR client credentials from file
  local client_file="$BCQ_GLOBAL_CONFIG_DIR/$BCQ_CLIENT_FILE"
  if [[ ! -f "$client_file" ]]; then
    return 1
  fi

  client_id=$(jq -r '.client_id' "$client_file")
  client_secret=$(jq -r '.client_secret // ""' "$client_file")

  # Only client_id is required (public clients may not have secret)
  [[ -n "$client_id" ]]
}

_load_launchpad_client() {
  # Load Launchpad client credentials from environment or config
  # Launchpad requires pre-registered clients (no DCR)

  # Priority: env vars > config file
  if [[ -n "$BCQ_CLIENT_ID" ]] && [[ -n "$BCQ_CLIENT_SECRET" ]]; then
    client_id="$BCQ_CLIENT_ID"
    client_secret="$BCQ_CLIENT_SECRET"
    debug "Loaded Launchpad credentials from environment"
    return 0
  fi

  # Try config file
  local config_client_id config_client_secret
  config_client_id=$(get_config "oauth_client_id" "")
  config_client_secret=$(get_config "oauth_client_secret" "")

  if [[ -n "$config_client_id" ]] && [[ -n "$config_client_secret" ]]; then
    client_id="$config_client_id"
    client_secret="$config_client_secret"
    debug "Loaded Launchpad credentials from config"
    return 0
  fi

  return 1
}

_generate_code_verifier() {
  # Generate random 43-128 character string for PKCE (RFC 7636)
  # Use extra bytes to ensure we have enough after removing invalid chars
  # Valid chars: [A-Za-z0-9._~-]
  local verifier
  while true; do
    verifier=$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=' | cut -c1-43)
    if [[ ${#verifier} -ge 43 ]]; then
      echo "$verifier"
      return
    fi
  done
}

_generate_code_challenge() {
  local verifier="$1"
  # S256: BASE64URL(SHA256(verifier))
  echo -n "$verifier" | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '='
}

_generate_state() {
  openssl rand -hex 16
}

_open_browser() {
  local url="$1"

  case "$(uname -s)" in
    Darwin) open "$url" ;;
    Linux)
      if command -v xdg-open &>/dev/null; then
        xdg-open "$url"
      elif command -v gnome-open &>/dev/null; then
        gnome-open "$url"
      else
        warn "Could not open browser automatically"
      fi
      ;;
    MINGW*|CYGWIN*) start "$url" ;;
    *) warn "Could not open browser automatically" ;;
  esac
}

_wait_for_callback() {
  local expected_state="$1"
  local timeout_secs=120

  # Check dependencies
  if ! command -v nc &>/dev/null; then
    die "netcat (nc) is required for OAuth callback" $EXIT_USAGE \
      "Install: brew install netcat (macOS) or apt install netcat (Linux)"
  fi

  if ! command -v timeout &>/dev/null && ! command -v gtimeout &>/dev/null; then
    die "timeout is required for OAuth callback" $EXIT_USAGE \
      "Install: brew install coreutils (macOS, provides gtimeout)"
  fi

  local timeout_cmd="timeout"
  command -v timeout &>/dev/null || timeout_cmd="gtimeout"

  info "Waiting for authorization (timeout: ${timeout_secs}s)..."

  # Create temp file for HTTP response (piping to nc doesn't block on macOS BSD nc)
  local http_response_file
  http_response_file=$(mktemp)
  printf 'HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html><body><h1>Authorization successful!</h1><p>You can close this window.</p></body></html>' > "$http_response_file"

  local response exit_code=0
  response=$("$timeout_cmd" "$timeout_secs" bash -c '
    response_file="'"$http_response_file"'"
    port="'"$BCQ_REDIRECT_PORT"'"
    request_file=$(mktemp)
    trap "rm -f $request_file" EXIT

    while true; do
      # Open response file for reading on fd3
      exec 3<"$response_file" || exit 1

      # nc -l PORT: listen for one connection
      # <&3 redirects stdin from fd3 (response file) - nc sends this to client
      # nc outputs what client sends (the HTTP request) to stdout
      # Capture to file to avoid SIGPIPE from head -1 killing nc before response is sent
      nc -l "$port" <&3 > "$request_file" 2>/dev/null

      # Close the file descriptor
      exec 3<&-

      # Read the first line (HTTP request line) from captured output
      request=$(head -1 "$request_file")

      if [[ "$request" == *"GET /callback"* ]]; then
        echo "$request"
        break
      fi

      # Small delay before retry to avoid tight loop
      sleep 0.1
    done
  ') || exit_code=$?

  rm -f "$http_response_file"

  if [[ -z "$response" ]] || [[ $exit_code -ne 0 ]]; then
    die "Authorization timed out" $EXIT_AUTH
  fi

  # Parse callback URL
  local query_string
  query_string=$(echo "$response" | sed -n 's/.*GET \/callback?\([^ ]*\).*/\1/p')

  local code state
  code=$(echo "$query_string" | tr '&' '\n' | grep '^code=' | cut -d= -f2)
  state=$(echo "$query_string" | tr '&' '\n' | grep '^state=' | cut -d= -f2)

  # URL decode the code (may contain encoded characters)
  code=$(printf '%b' "${code//%/\\x}")

  debug "Received auth code: $code"
  debug "Received state: $state"

  if [[ "$state" != "$expected_state" ]]; then
    die "State mismatch - possible CSRF attack" $EXIT_AUTH
  fi

  if [[ -z "$code" ]]; then
    local error
    error=$(echo "$query_string" | tr '&' '\n' | grep '^error=' | cut -d= -f2)
    die "Authorization failed: ${error:-unknown error}" $EXIT_AUTH
  fi

  echo "$code"
}

_exchange_code() {
  local code="$1"
  local code_verifier="$2"
  local client_id="$3"
  local client_secret="$4"
  local oauth_type="${5:-bc3}"

  local token_endpoint
  token_endpoint=$(_token_endpoint)

  debug "Exchanging code at: $token_endpoint (oauth_type=$oauth_type)"

  local response

  if [[ "$oauth_type" == "bc3" ]]; then
    # BC3 OAuth 2.1 token exchange with PKCE
    debug "Code verifier: $code_verifier"
    debug "Code verifier length: ${#code_verifier}"

    # Build curl args - only include client_secret for confidential clients
    local curl_args=(
      -s -X POST
      -H "Content-Type: application/x-www-form-urlencoded"
      --data-urlencode "grant_type=authorization_code"
      --data-urlencode "code=$code"
      --data-urlencode "redirect_uri=$BCQ_REDIRECT_URI"
      --data-urlencode "client_id=$client_id"
      --data-urlencode "code_verifier=$code_verifier"
    )

    # Only include client_secret for confidential clients (non-empty secret)
    if [[ -n "$client_secret" ]]; then
      curl_args+=(--data-urlencode "client_secret=$client_secret")
    fi

    response=$(curl "${curl_args[@]}" "$token_endpoint")
  else
    # Launchpad OAuth 2 token exchange (no PKCE, different params)
    response=$(curl -s -X POST \
      -H "Content-Type: application/x-www-form-urlencoded" \
      --data-urlencode "type=web_server" \
      --data-urlencode "code=$code" \
      --data-urlencode "redirect_uri=$BCQ_REDIRECT_URI" \
      --data-urlencode "client_id=$client_id" \
      --data-urlencode "client_secret=$client_secret" \
      "$token_endpoint")
  fi

  local access_token refresh_token expires_in scope
  access_token=$(echo "$response" | jq -r '.access_token // empty')
  refresh_token=$(echo "$response" | jq -r '.refresh_token // empty')
  expires_in=$(echo "$response" | jq -r '.expires_in // 7200')
  scope=$(echo "$response" | jq -r '.scope // empty')

  if [[ -z "$access_token" ]]; then
    debug "Token response: $response"
    return 1
  fi

  local expires_at
  expires_at=$(($(date +%s) + expires_in))

  # Store oauth_type and token_endpoint in credentials for proper refresh handling
  # Storing token_endpoint ensures refresh works even if discovery fails later
  local creds
  creds=$(jq -n \
    --arg access_token "$access_token" \
    --arg refresh_token "$refresh_token" \
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

  save_credentials "$creds"
}

_discover_accounts() {
  local token
  token=$(get_access_token) || return 1

  # Authorization self-introspection - returns token info including accessible accounts
  # Uses API host since DCR clients are untrusted and only allowed on API endpoints
  local authorization_endpoint="$BCQ_API_URL/authorization.json"

  debug "Fetching authorization from: $authorization_endpoint"

  local response http_code
  response=$(curl -s -w '\n%{http_code}' \
    -H "Authorization: Bearer $token" \
    -H "User-Agent: $BCQ_USER_AGENT" \
    -H "Accept: application/json" \
    "$authorization_endpoint")

  http_code=$(echo "$response" | tail -n1)
  response=$(echo "$response" | sed '$d')

  if [[ "$http_code" != "200" ]]; then
    debug "Authorization fetch failed (HTTP $http_code): $response"
    return 1
  fi

  local accounts
  # Extract accounts from authorization response; queenbee_id is used in URLs
  accounts=$(echo "$response" | jq '[.accounts[] | {id: .queenbee_id, name: .name, href: .href}]')

  if [[ "$accounts" != "[]" ]] && [[ "$accounts" != "null" ]]; then
    save_accounts "$accounts"
    return 0
  fi

  return 1
}

_select_account() {
  local accounts
  accounts=$(load_accounts)

  local count
  count=$(echo "$accounts" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    warn "No Basecamp accounts found"
    return
  fi

  if [[ "$count" -eq 1 ]]; then
    local account_id account_name
    account_id=$(echo "$accounts" | jq -r '.[0].id')
    account_name=$(echo "$accounts" | jq -r '.[0].name')
    set_global_config "account_id" "$account_id"
    info "Selected account: $account_name (#$account_id)"
  elif [[ "$count" -gt 1 ]]; then
    # Multiple accounts - let user choose
    echo "Multiple Basecamp accounts found:"
    echo
    echo "$accounts" | jq -r 'to_entries | .[] | "  \(.key + 1). \(.value.name) (#\(.value.id))"'
    echo

    local choice
    read -rp "Select account (1-$count): " choice

    if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= count )); then
      local account_id account_name
      account_id=$(echo "$accounts" | jq -r ".[$((choice - 1))].id")
      account_name=$(echo "$accounts" | jq -r ".[$((choice - 1))].name")
      set_global_config "account_id" "$account_id"
      info "Selected account: $account_name (#$account_id)"
    else
      warn "Invalid choice, using first account"
      local account_id
      account_id=$(echo "$accounts" | jq -r '.[0].id')
      set_global_config "account_id" "$account_id"
    fi
  fi

  # Save the API URLs so bcq knows which server to talk to
  # This ensures tokens obtained from dev servers talk to dev servers
  set_global_config "base_url" "$BCQ_BASE_URL"
  set_global_config "api_url" "$BCQ_API_URL"
  debug "Saved API URLs: base=$BCQ_BASE_URL api=$BCQ_API_URL"
}


# URL encoding helper

urlencode() {
  local string="$1"
  local strlen=${#string}
  local encoded=""
  local pos c o

  for (( pos=0 ; pos<strlen ; pos++ )); do
    c=${string:$pos:1}
    case "$c" in
      [-_.~a-zA-Z0-9]) o="$c" ;;
      *) printf -v o '%%%02x' "'$c" ;;
    esac
    encoded+="$o"
  done
  echo "$encoded"
}
