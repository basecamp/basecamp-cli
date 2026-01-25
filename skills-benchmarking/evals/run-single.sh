#!/usr/bin/env bash
# Quick single-run eval for rapid iteration
# Usage: ./evals/run-single.sh <strategy> [model] [task]

set -euo pipefail
cd "$(dirname "$0")/.."

strategy="${1:?Usage: run-single.sh <strategy> [model] [task]}"
model="${2:-claude-sonnet}"
task="${3:-12}"

source .env 2>/dev/null || true
./reset.sh >/dev/null 2>&1

echo "=== $strategy / $model / task $task ==="
result=$(./harness/run.sh --strategy "$strategy" --task "$task" --model "$model" 2>&1)

# Extract key metrics
success=$(echo "$result" | grep -o '"success": [a-z]*' | head -1 | awk '{print $2}')
http=$(echo "$result" | grep -o '"requests": [0-9]*' | grep -o '[0-9]*')
tokens=$(echo "$result" | grep -o '"total": [0-9]*' | head -1 | grep -o '[0-9]*')
turns=$(echo "$result" | grep -o '"turns": [0-9]*' | grep -o '[0-9]*')

if [[ "$success" == "true" ]]; then
  echo "✓ PASS  http=$http  tokens=$tokens  turns=$turns"
else
  echo "✗ FAIL  http=$http  tokens=$tokens  turns=$turns"
  # Show failure reason
  echo "$result" | grep -E "(FAIL:|Error)" | head -3
fi
