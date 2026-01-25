#!/usr/bin/env bash
# Quick 3-run eval for statistical signal
# Usage: ./evals/run-quick.sh <strategy> [model] [task]

set -euo pipefail
cd "$(dirname "$0")/.."

strategy="${1:?Usage: run-quick.sh <strategy> [model] [task]}"
model="${2:-claude-sonnet}"
task="${3:-12}"
runs="${4:-3}"

source .env 2>/dev/null || true

echo "=== $strategy / $model / task $task ($runs runs) ==="

pass=0
total_http=0
total_tokens=0

for i in $(seq 1 "$runs"); do
  ./reset.sh >/dev/null 2>&1
  result=$(./harness/run.sh --strategy "$strategy" --task "$task" --model "$model" 2>&1)

  success=$(echo "$result" | grep -o '"success": [a-z]*' | head -1 | awk '{print $2}')
  http=$(echo "$result" | grep -o '"requests": [0-9]*' | grep -o '[0-9]*' || echo "0")
  tokens=$(echo "$result" | grep -o '"total": [0-9]*' | head -1 | grep -o '[0-9]*' || echo "0")

  if [[ "$success" == "true" ]]; then
    echo "  Run $i: ✓ PASS  http=$http  tokens=$tokens"
    ((pass++))
  else
    echo "  Run $i: ✗ FAIL  http=$http  tokens=$tokens"
  fi

  ((total_http += http)) || true
  ((total_tokens += tokens)) || true
done

echo "---"
echo "Result: $pass/$runs pass ($(( pass * 100 / runs ))%)"
echo "Avg: http=$((total_http / runs))  tokens=$((total_tokens / runs))"
