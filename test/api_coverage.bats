#!/usr/bin/env bats
# api_coverage.bats - API coverage verification tests
#
# These tests verify that bcq implements all documented Basecamp API endpoints.
# They are skipped if bc3-api is not available locally.

setup() {
  export BCQ_ROOT="${BATS_TEST_DIRNAME}/.."
  export PATH="$BCQ_ROOT/bin:$PATH"

  # Check for bc3-api
  BC3_API_DIR="${BC3_API_DIR:-$HOME/Work/basecamp/bc3-api}"
  if [[ ! -d "$BC3_API_DIR/sections" ]]; then
    skip "bc3-api not found at $BC3_API_DIR"
  fi
  export BC3_API_DIR
}

@test "extract_docs.sh produces valid JSON" {
  run "$BCQ_ROOT/test/api_coverage/extract_docs.sh"
  [ "$status" -eq 0 ]

  # Validate JSON
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "extract_impl.sh produces valid JSON" {
  run "$BCQ_ROOT/test/api_coverage/extract_impl.sh"
  [ "$status" -eq 0 ]

  # Validate JSON
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "compare.sh runs without error" {
  run "$BCQ_ROOT/test/api_coverage/compare.sh"
  [ "$status" -eq 0 ]
}

@test "compare.sh --json produces valid JSON" {
  run "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  # Validate JSON
  echo "$output" | jq . > /dev/null
  [ "$?" -eq 0 ]
}

@test "coverage is at least 85%" {
  run "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  coverage=$(echo "$output" | jq -r '.coverage_percentage')
  [ "$coverage" -ge 85 ]
}

@test "documented endpoints count is reasonable (>100)" {
  run "$BCQ_ROOT/test/api_coverage/compare.sh" --json
  [ "$status" -eq 0 ]

  count=$(echo "$output" | jq -r '.documented_endpoints')
  [ "$count" -gt 100 ]
}

@test "exclusions file exists and is readable" {
  [ -f "$BCQ_ROOT/test/api_coverage/exclusions.txt" ]
}

@test "excluded sections are not in documented endpoints" {
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
