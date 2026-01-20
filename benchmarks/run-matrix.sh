#!/usr/bin/env bash
# Run benchmark matrix and collect results
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$BENCH_DIR"

source ./env.sh

RESULTS_FILE="$BENCH_DIR/results/matrix-$(date +%Y%m%d-%H%M%S).json"
TASKS=(01 02 03 04 05 06 07 08 09 10 11)

# Initialize results array
echo '[]' > "$RESULTS_FILE"

run_task() {
  local task_id="$1"
  local strategy="$2"
  local run_num="$3"

  echo "[matrix] Task $task_id, Strategy: $strategy, Run: $run_num"

  # Setup strategy
  ./harness.sh --task "$task_id" --strategy "$strategy" --setup-only 2>/dev/null || true

  local start_ms=$(date +%s%3N)
  local success=false
  local error_msg=""

  # Execute task based on strategy
  # (This is where agent execution happens - for now just validate existing state)

  local end_ms=$(date +%s%3N)
  local duration=$((end_ms - start_ms))

  # Record result
  local result=$(jq -n \
    --arg task "$task_id" \
    --arg strat "$strategy" \
    --argjson run "$run_num" \
    --argjson duration "$duration" \
    --argjson success "$success" \
    '{task: $task, strategy: $strat, run: $run, duration_ms: $duration, success: $success}')

  jq ". + [$result]" "$RESULTS_FILE" > "$RESULTS_FILE.tmp" && mv "$RESULTS_FILE.tmp" "$RESULTS_FILE"

  # Reset
  ./reset.sh --quiet 2>/dev/null || true
}

echo "[matrix] Starting benchmark matrix..."
echo "[matrix] Results: $RESULTS_FILE"

for task in "${TASKS[@]}"; do
  for strategy in bcq-full api-docs-with-curl-examples; do
    run_task "$task" "$strategy" 1
  done
done

echo "[matrix] Complete. Results in $RESULTS_FILE"
