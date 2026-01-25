#!/usr/bin/env bash
# Compare two strategies side-by-side
# Usage: ./evals/compare.sh <strategy-a> <strategy-b> [model] [runs]

set -euo pipefail
cd "$(dirname "$0")/.."

a="${1:?Usage: compare.sh <strategy-a> <strategy-b> [model] [runs]}"
b="${2:?Usage: compare.sh <strategy-a> <strategy-b> [model] [runs]}"
model="${3:-claude-sonnet}"
runs="${4:-3}"

source .env 2>/dev/null || true

echo "=== Comparing: $a vs $b ($model, $runs runs each) ==="
echo ""

for strategy in "$a" "$b"; do
  pass=0
  total_http=0
  total_tokens=0

  for i in $(seq 1 "$runs"); do
    ./reset.sh >/dev/null 2>&1
    result=$(./harness/run.sh --strategy "$strategy" --task 12 --model "$model" 2>&1)

    success=$(echo "$result" | grep -o '"success": [a-z]*' | head -1 | awk '{print $2}')
    http=$(echo "$result" | grep -o '"requests": [0-9]*' | grep -o '[0-9]*' || echo "0")
    tokens=$(echo "$result" | grep -o '"total": [0-9]*' | head -1 | grep -o '[0-9]*' || echo "0")

    [[ "$success" == "true" ]] && ((pass++))
    ((total_http += http)) || true
    ((total_tokens += tokens)) || true
  done

  pct=$((pass * 100 / runs))
  avg_http=$((total_http / runs))
  avg_tokens=$((total_tokens / runs))

  printf "%-20s %d/%d (%3d%%)  avg_http=%-4d  avg_tokens=%d\n" \
    "$strategy:" "$pass" "$runs" "$pct" "$avg_http" "$avg_tokens"
done
