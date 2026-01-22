#!/usr/bin/env bats
# api_coverage.bats - API coverage verification tests
#
# These tests verify that bcq implements all documented Basecamp API endpoints.
# By default, fetches from GitHub. Set BC3_API_DIR for local clone.
# Tests skip gracefully if GitHub API is rate-limited.

# Require bats 1.5.0+ for --separate-stderr support
bats_require_minimum_version 1.5.0

setup() {
  export BCQ_ROOT="${BATS_TEST_DIRNAME}/.."
  export PATH="$BCQ_ROOT/bin:$PATH"

  # Use local bc3-api clone if available, otherwise GitHub fetch (default)
  if [[ -n "${BC3_API_DIR:-}" ]] && [[ -d "$BC3_API_DIR/sections" ]]; then
    export BC3_API_DIR
  else
    unset BC3_API_DIR  # Let scripts use GitHub fetch
  fi
}

# Helper: check if docs can be fetched with sufficient data
# Returns false if rate-limited (partial data) or network error
docs_available() {
  local output count
  output=$("$BCQ_ROOT/test/api_coverage/extract_docs.sh" 2>/dev/null) || return 1
  count=$(echo "$output" | jq -r 'length' 2>/dev/null) || return 1
  # Need at least 50 endpoints to consider docs "available"
  # (full bc3-api has ~130 endpoints, partial fetch indicates rate limiting)
  [[ "$count" -gt 50 ]]
}

@test "extract_docs.sh produces valid JSON" {
  # Use --separate-stderr to prevent stderr from corrupting JSON output
  run --separate-stderr "$BCQ_ROOT/test/api_coverage/extract_docs.sh"
  if [ "$status" -ne 0 ]; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  # Validate JSON (output contains only stdout now)
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "extract_impl.sh produces valid JSON" {
  run --separate-stderr "$BCQ_ROOT/test/api_coverage/extract_impl.sh"
  [ "$status" -eq 0 ]

  # Validate JSON
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "compare.sh runs without error" {
  if ! docs_available; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  run "$BCQ_ROOT/test/api_coverage/compare.sh"
  [ "$status" -eq 0 ]
}

@test "compare.sh --json produces valid JSON" {
  if ! docs_available; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  # Use --separate-stderr to prevent stderr from corrupting JSON output
  run --separate-stderr "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  # Validate JSON
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "coverage is at least 85%" {
  if ! docs_available; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  run --separate-stderr "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  coverage=$(echo "$output" | jq -r '.coverage_percentage')
  [ "$coverage" -ge 85 ]
}

@test "documented endpoints count is reasonable (>100)" {
  if ! docs_available; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  run --separate-stderr "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  count=$(echo "$output" | jq -r '.documented_endpoints')
  [ "$count" -gt 100 ]
}

@test "exclusions file exists and is readable" {
  [ -f "$BCQ_ROOT/test/api_coverage/exclusions.txt" ]
}

@test "excluded sections are not in documented endpoints" {
  if ! docs_available; then
    skip "GitHub API unavailable (rate limited or network error)"
  fi

  # Read exclusions
  while IFS= read -r line; do
    [[ "$line" =~ ^#.*$ || -z "$line" ]] && continue
    excluded+=("$line")
  done < "$BCQ_ROOT/test/api_coverage/exclusions.txt"

  # Check that excluded sources don't appear in output
  docs_output=$("$BCQ_ROOT/test/api_coverage/extract_docs.sh")
  for excl in "${excluded[@]}"; do
    if echo "$docs_output" | grep -q "\"source\":\"${excl}.md\""; then
      fail "Excluded section $excl appears in documented endpoints"
    fi
  done
}
