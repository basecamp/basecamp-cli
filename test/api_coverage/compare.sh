#!/usr/bin/env bash
# compare.sh - Compare documented and implemented API endpoints
#
# Usage:
#   ./compare.sh                  Show coverage summary
#   ./compare.sh --check-missing  Exit 1 if any documented endpoints are missing
#   ./compare.sh --verify-counts  Exit 1 if counts don't match COVERAGE.md
#   ./compare.sh --json           Output as JSON
#   ./compare.sh --verbose        Show all endpoints

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BCQ_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Parse arguments
CHECK_MISSING=false
VERIFY_COUNTS=false
JSON_OUTPUT=false
VERBOSE=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check-missing) CHECK_MISSING=true; shift ;;
    --verify-counts) VERIFY_COUNTS=true; shift ;;
    --json) JSON_OUTPUT=true; shift ;;
    --verbose|-v) VERBOSE=true; shift ;;
    *) shift ;;
  esac
done

# Check if bc3-api is available
BC3_API_DIR="${BC3_API_DIR:-$HOME/Work/basecamp/bc3-api}"
if [[ ! -d "$BC3_API_DIR/sections" ]]; then
  if [[ "$JSON_OUTPUT" == "true" ]]; then
    echo '{"error": "bc3-api not found", "skip": true}'
  else
    echo "Warning: bc3-api not found at $BC3_API_DIR" >&2
    echo "Skipping API coverage check" >&2
  fi
  exit 0
fi

# Extract endpoints
DOCS_JSON=$("$SCRIPT_DIR/extract_docs.sh")
IMPL_JSON=$("$SCRIPT_DIR/extract_impl.sh")

# Normalize and deduplicate for comparison
normalize_endpoints() {
  local json="$1"
  echo "$json" | jq -r '.[].method + " " + .[].path' 2>/dev/null | sort -u
}

# Get unique documented endpoints (method + normalized path)
# Redirect jq stderr to /dev/null to prevent warnings from corrupting output in CI
DOCS_ENDPOINTS=$(echo "$DOCS_JSON" | jq -r '.[] | .method + " " + .path' 2>/dev/null | sort -u)
IMPL_ENDPOINTS=$(echo "$IMPL_JSON" | jq -r '.[] | .method + " " + .path' 2>/dev/null | sort -u)

# Count endpoints (|| true prevents exit on grep finding 0 matches)
# Ensure counts are always valid integers for jq
DOCS_COUNT=$(echo "$DOCS_ENDPOINTS" | grep -c . || true)
[[ "$DOCS_COUNT" =~ ^[0-9]+$ ]] || DOCS_COUNT=0
IMPL_COUNT=$(echo "$IMPL_ENDPOINTS" | grep -c . || true)
[[ "$IMPL_COUNT" =~ ^[0-9]+$ ]] || IMPL_COUNT=0

# Find missing (in docs but not implemented)
MISSING=$(comm -23 <(echo "$DOCS_ENDPOINTS") <(echo "$IMPL_ENDPOINTS") || true)
MISSING_COUNT=$(echo "$MISSING" | grep -c . || true)
[[ "$MISSING_COUNT" =~ ^[0-9]+$ ]] || MISSING_COUNT=0

# Find extra (implemented but not in docs)
EXTRA=$(comm -13 <(echo "$DOCS_ENDPOINTS") <(echo "$IMPL_ENDPOINTS") || true)
EXTRA_COUNT=$(echo "$EXTRA" | grep -c . || true)
[[ "$EXTRA_COUNT" =~ ^[0-9]+$ ]] || EXTRA_COUNT=0

# Coverage percentage
if [[ "$DOCS_COUNT" -gt 0 ]]; then
  COVERED=$((DOCS_COUNT - MISSING_COUNT))
  COVERAGE_PCT=$((COVERED * 100 / DOCS_COUNT))
else
  COVERED=0
  COVERAGE_PCT=100
fi

if [[ "$JSON_OUTPUT" == "true" ]]; then
  # JSON output
  MISSING_JSON="[]"
  if [[ -n "$MISSING" ]] && [[ "$MISSING_COUNT" -gt 0 ]]; then
    MISSING_JSON=$(echo "$MISSING" | jq -R -s 'split("\n") | map(select(length > 0))' 2>/dev/null)
  fi

  EXTRA_JSON="[]"
  if [[ -n "$EXTRA" ]] && [[ "$EXTRA_COUNT" -gt 0 ]]; then
    EXTRA_JSON=$(echo "$EXTRA" | jq -R -s 'split("\n") | map(select(length > 0))' 2>/dev/null)
  fi

  # Redirect jq stderr to prevent warnings from corrupting JSON output
  jq -n \
    --argjson docs_count "$DOCS_COUNT" \
    --argjson impl_count "$IMPL_COUNT" \
    --argjson missing_count "$MISSING_COUNT" \
    --argjson coverage_pct "$COVERAGE_PCT" \
    --argjson missing "$MISSING_JSON" \
    --argjson extra "$EXTRA_JSON" \
    '{
      documented_endpoints: $docs_count,
      implemented_endpoints: $impl_count,
      missing_endpoints: $missing_count,
      coverage_percentage: $coverage_pct,
      missing: $missing,
      extra: $extra
    }' 2>/dev/null
else
  # Human-readable output
  echo "API Coverage Report"
  echo "==================="
  echo ""
  echo "Documented endpoints:  $DOCS_COUNT"
  echo "Implemented endpoints: $IMPL_COUNT"
  echo "Missing endpoints:     $MISSING_COUNT"
  echo "Coverage:              $COVERAGE_PCT%"
  echo ""

  if [[ "$MISSING_COUNT" -gt 0 ]]; then
    echo "Missing endpoints:"
    echo "$MISSING" | sed 's/^/  /'
    echo ""
  fi

  if [[ "$VERBOSE" == "true" ]] && [[ "$EXTRA_COUNT" -gt 0 ]]; then
    echo "Extra endpoints (in implementation but not in docs):"
    echo "$EXTRA" | sed 's/^/  /'
    echo ""
  fi
fi

# Exit codes for CI checks
if [[ "$CHECK_MISSING" == "true" ]] && [[ "$MISSING_COUNT" -gt 0 ]]; then
  exit 1
fi

if [[ "$VERIFY_COUNTS" == "true" ]]; then
  # Parse COVERAGE.md for expected counts
  COVERAGE_MD="$BCQ_ROOT/COVERAGE.md"
  if [[ -f "$COVERAGE_MD" ]]; then
    # Look for "130/130" or similar pattern
    EXPECTED=$(grep -oE '[0-9]+/[0-9]+ \(100%\)' "$COVERAGE_MD" | head -1 | cut -d'/' -f1)
    if [[ -n "$EXPECTED" ]] && [[ "$COVERED" -ne "$EXPECTED" ]]; then
      echo "Error: Coverage mismatch" >&2
      echo "  COVERAGE.md claims: $EXPECTED endpoints" >&2
      echo "  Actual covered:     $COVERED endpoints" >&2
      exit 1
    fi
  fi
fi

exit 0
