#!/usr/bin/env bats
# api_errors.bats - Tests for API error handling and retry behavior

load test_helper

# Create a mock curl that returns specific HTTP status codes
setup_mock_curl() {
  local status_code="$1"
  local response_body="${2:-{}}"

  mkdir -p "$BATS_TEST_TMPDIR/bin"

  cat > "$BATS_TEST_TMPDIR/bin/curl" <<EOF
#!/bin/bash
# Mock curl - returns predefined status code
# Write empty headers to the -D file
for arg in "\$@"; do
  if [[ "\$prev" == "-D" ]]; then
    echo "HTTP/1.1 $status_code" > "\$arg"
  fi
  prev="\$arg"
done
# Output body followed by status code (matching -w '%{http_code}')
echo '$response_body'
echo '$status_code'
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/curl"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

# Create mock curl that returns different codes on successive calls
setup_mock_curl_sequence() {
  local codes=("$@")
  local tmpdir="$BATS_TEST_TMPDIR"

  mkdir -p "$tmpdir/bin"

  # Write the sequence of status codes
  printf '%s\n' "${codes[@]}" > "$tmpdir/curl_codes"
  echo "0" > "$tmpdir/curl_call_count"

  cat > "$tmpdir/bin/curl" <<EOF
#!/bin/bash
# Track call count in a file
COUNT_FILE="$tmpdir/curl_call_count"
count=\$(cat "\$COUNT_FILE")
echo \$((count + 1)) > "\$COUNT_FILE"

# Read the status code for this call (1-indexed)
CODES_FILE="$tmpdir/curl_codes"
status=\$(sed -n "\$((count + 1))p" "\$CODES_FILE" 2>/dev/null)
[[ -z "\$status" ]] && status="200"

# Write headers
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "-D" ]]; then
    echo "HTTP/1.1 \$status" > "\$arg"
  fi
  prev="\$arg"
done

echo '{}'
echo "\$status"
EOF
  chmod +x "$tmpdir/bin/curl"

  export PATH="$tmpdir/bin:$PATH"
}

get_curl_call_count() {
  cat "$BATS_TEST_TMPDIR/curl_call_count" 2>/dev/null || echo "0"
}


# === 500 Error Tests ===

@test "500 error fails immediately without retry" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl 500 '{"error": "Internal Server Error"}'

  run bcq projects list
  assert_failure
  assert_output_contains "Server error (500)"
  assert_output_not_contains "retrying"
}

@test "500 error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl 500 '{"error": "Internal Server Error"}'

  run bcq projects list
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'api_error'
  assert_output_contains "internal error"
}


# === 502/503/504 Retry Tests ===

@test "502 error triggers retry" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  # First call 502, second call 200
  setup_mock_curl_sequence 502 200
  export BCQ_BASE_DELAY=0  # No delay in tests

  run bcq projects list
  # Should succeed after retry
  [[ "$(get_curl_call_count)" -ge 2 ]]
}

@test "503 error triggers retry" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl_sequence 503 200
  export BCQ_BASE_DELAY=0

  run bcq projects list
  [[ "$(get_curl_call_count)" -ge 2 ]]
}

@test "504 error triggers retry" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl_sequence 504 200
  export BCQ_BASE_DELAY=0

  run bcq projects list
  [[ "$(get_curl_call_count)" -ge 2 ]]
}

@test "gateway error shows retry message" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl_sequence 502 502 502 502 502
  export BCQ_BASE_DELAY=0
  export BCQ_MAX_RETRIES=2

  run bcq projects list
  assert_failure
  assert_output_contains "Gateway error"
  assert_output_contains "retrying"
}

@test "gateway errors exhaust retries then fail" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl_sequence 503 503 503 503 503
  export BCQ_BASE_DELAY=0
  export BCQ_MAX_RETRIES=3

  run bcq projects list
  assert_failure
  assert_output_contains "failed after"
  assert_output_contains "retries"
}


# === Other Error Codes ===

@test "404 error fails immediately" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl 404 '{"error": "Not found"}'

  run bcq projects list
  assert_failure
  assert_json_value '.code' 'not_found'
}

@test "401 error indicates auth required" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl 401 '{"error": "Unauthorized"}'

  run bcq projects list
  assert_failure
  assert_json_value '.code' 'auth_required'
}

@test "403 error indicates forbidden" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  setup_mock_curl 403 '{"error": "Forbidden"}'

  run bcq projects list
  assert_failure
  assert_json_value '.code' 'forbidden'
}
