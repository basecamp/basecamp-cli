#!/usr/bin/env bash
# Run all eval cases and aggregate results
# Exit code 2 (SKIP) is excluded from pass rate

set -euo pipefail

MODEL="${1:-gpt-4o-mini}"
ITERATIONS="${2:-1}"
PROMPT="${3:-api-with-guide}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_DIR="$SCRIPT_DIR/cases"

# Source env if exists
[[ -f "$SCRIPT_DIR/../.env" ]] && source "$SCRIPT_DIR/../.env"

passes=0
fails=0
skips=0

echo "Running evals: model=$MODEL, iterations=$ITERATIONS"
echo "=================================================="

for case_file in "$CASES_DIR"/*.yml; do
  case_name=$(basename "$case_file" .yml)
  case_passes=0
  case_fails=0
  case_skips=0

  for ((i=1; i<=ITERATIONS; i++)); do
    set +e
    ruby "$SCRIPT_DIR/harness/agent_runner.rb" "$case_file" "$SCRIPT_DIR/../prompts/$PROMPT.md" -m "$MODEL" > /dev/null 2>&1
    exit_code=$?
    set -e

    case $exit_code in
      0) ((case_passes++)); ((passes++)) ;;
      1) ((case_fails++)); ((fails++)) ;;
      2) ((case_skips++)); ((skips++)) ;;  # Infra error - exclude from rate
    esac
  done

  # Report per-case
  total=$((case_passes + case_fails))
  if [[ $total -gt 0 ]]; then
    rate=$((case_passes * 100 / total))
    printf "%-30s %d/%d (%d%%)" "$case_name" "$case_passes" "$total" "$rate"
  else
    printf "%-30s SKIPPED" "$case_name"
  fi
  [[ $case_skips -gt 0 ]] && printf " [%d infra errors]" "$case_skips"
  echo ""
done

echo "=================================================="
total=$((passes + fails))
if [[ $total -gt 0 ]]; then
  rate=$((passes * 100 / total))
  echo "Overall: $passes/$total ($rate%)"
else
  echo "Overall: No valid runs (all skipped)"
fi
[[ $skips -gt 0 ]] && echo "Skipped: $skips (infrastructure errors, excluded from rate)"
