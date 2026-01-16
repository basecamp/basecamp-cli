#!/usr/bin/env bash
# Error injection management for benchmarks
# Creates a curl wrapper that injects errors based on configurable triggers
#
# Usage:
#   ./inject-proxy.sh setup 429 1                    # Inject one 429 on first request
#   ./inject-proxy.sh setup 429 1 --at-request 5    # Inject 429 on 5th request
#   ./inject-proxy.sh setup 429 1 --match "page="   # Inject 429 when URL contains "page="
#   ./inject-proxy.sh clear                          # Clear injection state
#   ./inject-proxy.sh status                         # Show current state
#
# Environment variables (alternative to flags):
#   BCQ_INJECT_AT_REQUEST=5    # Inject on 5th request
#   BCQ_INJECT_MATCH="page="   # Inject when URL matches pattern
#
# The inject-curl script (created by setup) should be used via PATH prepend

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INJECT_STATE_FILE="$BENCH_DIR/.inject-state"
INJECT_CURL_SCRIPT="$BENCH_DIR/inject-curl"
INJECT_COUNTER_FILE="$BENCH_DIR/.inject-counter"

# Create the inject-curl script that will be prepended to PATH
create_inject_curl() {
  cat > "$INJECT_CURL_SCRIPT" << 'SCRIPT'
#!/usr/bin/env bash
# Curl wrapper with error injection support
# Created by inject-proxy.sh
#
# Supports targeted injection:
# - at_request: inject on Nth request
# - match: inject when URL contains pattern

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INJECT_STATE_FILE="$BENCH_DIR/.inject-state"
INJECT_COUNTER_FILE="$BENCH_DIR/.inject-counter"
LOGFILE="${BCQ_BENCH_LOGFILE:-$BENCH_DIR/results/requests.log}"

# Find real curl
find_real_curl() {
  for candidate in /usr/bin/curl /opt/homebrew/bin/curl /usr/local/bin/curl; do
    if [[ -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  echo "curl"
}

REAL_CURL="$(find_real_curl)"

# Get and increment request counter (atomic via temp file)
get_request_number() {
  local counter=0
  if [[ -f "$INJECT_COUNTER_FILE" ]]; then
    counter=$(cat "$INJECT_COUNTER_FILE" 2>/dev/null || echo 0)
  fi
  counter=$((counter + 1))
  echo "$counter" > "$INJECT_COUNTER_FILE"
  echo "$counter"
}

# Check if we should inject an error
# Returns error code if should inject, returns 1 otherwise
check_injection() {
  local url="$1"
  local request_num="$2"

  if [[ ! -f "$INJECT_STATE_FILE" ]]; then
    return 1  # No injection configured
  fi

  # State file format: error_code remaining_count at_request match_pattern
  local error_code remaining at_request match_pattern
  IFS=' ' read -r error_code remaining at_request match_pattern < "$INJECT_STATE_FILE"

  if [[ "$remaining" -le 0 ]]; then
    return 1  # No more injections
  fi

  # Check targeting conditions
  local should_inject=false

  # at_request targeting: inject on specific request number
  if [[ -n "$at_request" ]] && [[ "$at_request" != "0" ]]; then
    if [[ "$request_num" -eq "$at_request" ]]; then
      should_inject=true
    fi
  # match targeting: inject when URL contains pattern
  elif [[ -n "$match_pattern" ]] && [[ "$match_pattern" != "-" ]]; then
    if [[ "$url" == *"$match_pattern"* ]]; then
      should_inject=true
    fi
  # No targeting: inject on any request
  else
    should_inject=true
  fi

  if [[ "$should_inject" == "true" ]]; then
    # Decrement remaining count
    echo "$error_code $((remaining - 1)) $at_request $match_pattern" > "$INJECT_STATE_FILE"
    echo "$error_code"
    return 0
  fi

  return 1
}

# Parse curl args to extract URL and method
parse_args() {
  url=""
  method="GET"
  local next_is_method=false

  for arg in "$@"; do
    if [[ "$next_is_method" == "true" ]]; then
      method="$arg"
      next_is_method=false
      continue
    fi

    case "$arg" in
      -X|--request) next_is_method=true ;;
      http://*|https://*) url="$arg" ;;
    esac
  done
}

# Get current time in milliseconds
now_ms() {
  if [[ "$(uname)" == "Darwin" ]]; then
    perl -MTime::HiRes=time -e 'printf "%.0f\n", time * 1000'
  else
    echo $(($(date +%s%N) / 1000000))
  fi
}

# Log a request
log_request() {
  local method="$1" url="$2" http_code="$3" duration_ms="$4" injected="${5:-false}"
  mkdir -p "$(dirname "$LOGFILE")"
  local redacted_url="${url/access_token=*/access_token=[REDACTED]}"
  echo "{\"ts\":$(now_ms),\"method\":\"$method\",\"url\":\"$redacted_url\",\"http_code\":\"$http_code\",\"duration_ms\":$duration_ms,\"injected\":$injected}" >> "$LOGFILE"
}

# Main
main() {
  parse_args "$@"

  local request_num
  request_num=$(get_request_number)

  local inject_code
  if inject_code=$(check_injection "$url" "$request_num"); then
    # Inject error response
    case "$inject_code" in
      401)
        log_request "$method" "$url" "401" "1" "true"
        echo '{"error": "The access token is invalid"}' >&2
        exit 22  # CURLE_HTTP_RETURNED_ERROR
        ;;
      429)
        log_request "$method" "$url" "429" "1" "true"
        echo "Retry-After: 2" >&2
        echo '{"error": "Rate limit exceeded"}' >&2
        exit 22
        ;;
    esac
  fi

  # No injection, pass through to real curl with logging
  local start_ms=$(now_ms)

  # Capture output and exit code
  local output exit_code=0
  output=$("$REAL_CURL" "$@" 2>&1) || exit_code=$?

  local end_ms=$(now_ms)
  local duration=$((end_ms - start_ms))

  # Try to extract HTTP code from curl output or assume 200 on success
  local http_code="000"
  if [[ $exit_code -eq 0 ]]; then
    http_code="200"
  elif [[ $exit_code -eq 22 ]]; then
    # HTTP error returned, try to parse from response
    http_code="4xx"
  fi

  log_request "$method" "$url" "$http_code" "$duration" "false"

  echo "$output"
  exit $exit_code
}

main "$@"
SCRIPT

  chmod +x "$INJECT_CURL_SCRIPT"
}

# Setup error injection
cmd_setup() {
  local error_code="${1:-}"
  local count="${2:-1}"
  shift 2 || true

  local at_request="0"
  local match_pattern="-"

  # Parse additional options
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --at-request)
        at_request="$2"
        shift 2
        ;;
      --match)
        match_pattern="$2"
        shift 2
        ;;
      *)
        echo "Unknown option: $1" >&2
        shift
        ;;
    esac
  done

  # Also check environment variables
  at_request="${BCQ_INJECT_AT_REQUEST:-$at_request}"
  match_pattern="${BCQ_INJECT_MATCH:-$match_pattern}"

  if [[ -z "$error_code" ]] || [[ ! "$error_code" =~ ^(401|429)$ ]]; then
    echo "Usage: $0 setup <401|429> [count] [--at-request N] [--match pattern]" >&2
    exit 1
  fi

  # Reset counter
  echo "0" > "$INJECT_COUNTER_FILE"

  # Write state: error_code remaining at_request match_pattern
  echo "$error_code $count $at_request $match_pattern" > "$INJECT_STATE_FILE"
  create_inject_curl

  echo "Injection configured: $count x $error_code"
  if [[ "$at_request" != "0" ]]; then
    echo "  Trigger: request #$at_request"
  elif [[ "$match_pattern" != "-" ]]; then
    echo "  Trigger: URL contains '$match_pattern'"
  else
    echo "  Trigger: first $count request(s)"
  fi
  echo "Add to PATH: export PATH=\"$BENCH_DIR:\$PATH\""
}

# Clear injection state
cmd_clear() {
  rm -f "$INJECT_STATE_FILE"
  rm -f "$INJECT_CURL_SCRIPT"
  rm -f "$INJECT_COUNTER_FILE"
  echo "Injection state cleared"
}

# Show current state
cmd_status() {
  if [[ -f "$INJECT_STATE_FILE" ]]; then
    local error_code remaining at_request match_pattern
    IFS=' ' read -r error_code remaining at_request match_pattern < "$INJECT_STATE_FILE"
    echo "Active injection: $remaining remaining x $error_code"
    if [[ "$at_request" != "0" ]]; then
      echo "  Trigger: request #$at_request"
    elif [[ "$match_pattern" != "-" ]]; then
      echo "  Trigger: URL contains '$match_pattern'"
    fi
  else
    echo "No injection configured"
  fi

  if [[ -f "$INJECT_COUNTER_FILE" ]]; then
    echo "Request counter: $(cat "$INJECT_COUNTER_FILE")"
  fi

  if [[ -x "$INJECT_CURL_SCRIPT" ]]; then
    echo "inject-curl script exists"
  else
    echo "inject-curl script does not exist"
  fi
}

# Main dispatcher
main() {
  local cmd="${1:-status}"
  shift || true

  case "$cmd" in
    setup) cmd_setup "$@" ;;
    clear) cmd_clear ;;
    status) cmd_status ;;
    *)
      echo "Usage: $0 <setup|clear|status> [args]" >&2
      exit 1
      ;;
  esac
}

main "$@"
