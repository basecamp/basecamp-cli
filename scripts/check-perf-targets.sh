#!/usr/bin/env bash
# Check benchmark results against performance targets
#
# Usage:
#   go test -bench=. -benchmem ./internal/... | ./scripts/check-perf-targets.sh
#   # or
#   ./scripts/check-perf-targets.sh < benchmark-results.txt
#   # or
#   ./scripts/check-perf-targets.sh benchmark-results.txt
#
# Exit codes:
#   0 - All targets passed
#   1 - One or more targets exceeded
#   2 - Configuration error
#
# Requires: yq (https://github.com/mikefarah/yq) for YAML parsing

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TARGETS_FILE="$PROJECT_ROOT/perf-targets.yaml"

# Colors for output (disable if not a terminal)
if [[ -t 1 ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    BLUE='\033[0;34m'
    NC='\033[0m'
else
    RED=''
    GREEN=''
    YELLOW=''
    BLUE=''
    NC=''
fi

# Check for yq
if ! command -v yq &> /dev/null; then
    echo "Error: yq is required but not installed." >&2
    echo "Install with: brew install yq" >&2
    exit 2
fi

# Check targets file exists
if [[ ! -f "$TARGETS_FILE" ]]; then
    echo "Error: Targets file not found: $TARGETS_FILE" >&2
    exit 2
fi

# Read benchmark input
if [[ $# -gt 0 && -f "$1" ]]; then
    BENCH_OUTPUT=$(cat "$1")
else
    BENCH_OUTPUT=$(cat)
fi

# Track results
PASSED=0
FAILED=0
CHECKED=0
MISSING=0

echo ""
echo -e "${BLUE}Performance Target Check${NC}"
echo "========================="
echo ""

# Get list of benchmark names from targets file
bench_names=$(yq -r '.targets | keys | .[]' "$TARGETS_FILE")

# Process each target
while IFS= read -r bench_name; do
    # Get target value
    target=$(yq -r ".targets[\"$bench_name\"].ns_per_op" "$TARGETS_FILE")

    if [[ "$target" == "null" || -z "$target" ]]; then
        continue
    fi

    # Find the benchmark result in output
    # Format: BenchmarkName-N    iterations    ns/op    ...
    # Use grep -F for fixed string matching (handles slashes in benchmark names)
    result_line=$(echo "$BENCH_OUTPUT" | grep -F "${bench_name}-" | head -1 || true)

    if [[ -z "$result_line" ]]; then
        echo -e "${YELLOW}SKIP${NC} $bench_name (not found in results)"
        MISSING=$((MISSING + 1))
        continue
    fi

    # Extract ns/op value (the number before "ns/op")
    actual=$(echo "$result_line" | awk '{for(i=1;i<=NF;i++) if($i=="ns/op") print $(i-1)}')

    if [[ -z "$actual" ]]; then
        echo -e "${YELLOW}SKIP${NC} $bench_name (could not parse ns/op)"
        MISSING=$((MISSING + 1))
        continue
    fi

    CHECKED=$((CHECKED + 1))

    # Compare using bc for floating point (handle scientific notation)
    # First normalize the number (remove scientific notation if present)
    actual_normalized=$(printf "%.2f" "$actual" 2>/dev/null || echo "$actual")

    if (( $(echo "$actual_normalized <= $target" | bc -l) )); then
        printf "${GREEN}PASS${NC} %s: %.0fns <= %dns\n" "$bench_name" "$actual_normalized" "$target"
        PASSED=$((PASSED + 1))
    else
        pct=$(echo "scale=1; ($actual_normalized - $target) / $target * 100" | bc)
        printf "${RED}FAIL${NC} %s: %.0fns > %dns (+%s%%)\n" "$bench_name" "$actual_normalized" "$target" "$pct"
        FAILED=$((FAILED + 1))
    fi
done <<< "$bench_names"

echo ""
echo "========================="
echo -e "Checked: $CHECKED | ${GREEN}Passed: $PASSED${NC} | ${RED}Failed: $FAILED${NC} | ${YELLOW}Skipped: $MISSING${NC}"
echo ""

if [[ $FAILED -gt 0 ]]; then
    echo -e "${RED}Performance gate FAILED${NC}"
    exit 1
else
    echo -e "${GREEN}Performance gate PASSED${NC}"
    exit 0
fi
