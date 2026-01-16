#!/usr/bin/env bash
# Curl wrapper that logs all requests for metrics
# Used by BOTH bcq and raw-api conditions for fair request counting
#
# To use: prepend to PATH so this gets called instead of real curl
# export PATH="$BENCH_DIR:$PATH"

set -euo pipefail

LOGFILE="${BCQ_BENCH_LOGFILE:-/tmp/bcq-bench-requests.log}"

# Find real curl (skip this script)
find_real_curl() {
  local self="$0"
  local IFS=':'
  for dir in $PATH; do
    local candidate="$dir/curl"
    if [[ -x "$candidate" ]] && [[ "$candidate" != "$self" ]] && [[ "$(basename "$candidate")" == "curl" ]]; then
      # Make sure it's not this script
      if ! grep -q "BCQ_BENCH_LOGFILE" "$candidate" 2>/dev/null; then
        echo "$candidate"
        return 0
      fi
    fi
  done
  # Fallback to standard locations
  for candidate in /usr/bin/curl /opt/homebrew/bin/curl /usr/local/bin/curl; do
    if [[ -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  echo "curl"  # Hope for the best
}

REAL_CURL="$(find_real_curl)"

# Parse curl args to extract URL and method
parse_args() {
  url=""
  method="GET"
  local next_is_method=false
  local next_is_data=false

  for arg in "$@"; do
    if [[ "$next_is_method" == "true" ]]; then
      method="$arg"
      next_is_method=false
      continue
    fi
    if [[ "$next_is_data" == "true" ]]; then
      next_is_data=false
      continue
    fi

    case "$arg" in
      -X|--request)
        next_is_method=true
        ;;
      -d|--data|--data-raw|--data-binary|--data-urlencode)
        next_is_data=true
        # If method not explicitly set, POST is implied
        [[ "$method" == "GET" ]] && method="POST"
        ;;
      http://*)
        url="$arg"
        ;;
      https://*)
        url="$arg"
        ;;
    esac
  done
}

# Redact sensitive data from URL
redact_url() {
  local url="$1"
  # Remove any query params that might contain tokens
  echo "$url" | sed -E 's/access_token=[^&]+/access_token=[REDACTED]/g'
}

# Get current time in milliseconds
now_ms() {
  if [[ "$(uname)" == "Darwin" ]]; then
    # macOS: use perl for millisecond precision
    perl -MTime::HiRes=time -e 'printf "%.0f\n", time * 1000'
  else
    # Linux: date supports %N for nanoseconds
    echo $(($(date +%s%N) / 1000000))
  fi
}

# Main execution
main() {
  parse_args "$@"

  # Create log directory if needed
  mkdir -p "$(dirname "$LOGFILE")"

  # Record start time
  local start_ms
  start_ms=$(now_ms)

  # Create temp file for headers
  local headers_file
  headers_file=$(mktemp)
  trap "rm -f '$headers_file'" EXIT

  # Execute real curl, capturing headers
  local output exit_code=0
  output=$("$REAL_CURL" -D "$headers_file" "$@" 2>&1) || exit_code=$?

  # Record end time
  local end_ms
  end_ms=$(now_ms)

  # Extract HTTP status code from headers
  local http_code="000"
  if [[ -f "$headers_file" ]]; then
    http_code=$(grep -oE '^HTTP/[0-9.]+ ([0-9]{3})' "$headers_file" | tail -1 | grep -oE '[0-9]{3}$' || echo "000")
  fi

  # Log request (with redacted URL)
  local redacted_url
  redacted_url=$(redact_url "$url")
  local duration_ms=$((end_ms - start_ms))

  echo "{\"ts\":$start_ms,\"method\":\"$method\",\"url\":\"$redacted_url\",\"http_code\":\"$http_code\",\"duration_ms\":$duration_ms}" >> "$LOGFILE"

  # Return original output
  echo "$output"
  exit $exit_code
}

main "$@"
