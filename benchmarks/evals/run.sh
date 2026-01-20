#!/usr/bin/env bash
# Run eval harness against case files
# Usage: ./run.sh <case.yml> [--json] [--script requests.json]

set -euo pipefail
cd "$(dirname "$0")"

case_file="${1:-}"
shift || true

if [[ -z "$case_file" ]]; then
  echo "Usage: ./run.sh <case.yml> [--json] [--script requests.json]"
  echo ""
  echo "Available cases:"
  for f in cases/*.yml; do
    name=$(basename "$f" .yml)
    echo "  $name"
  done
  exit 1
fi

# Resolve case file path
if [[ ! -f "$case_file" ]]; then
  case_file="cases/${case_file}.yml"
fi

if [[ ! -f "$case_file" ]]; then
  echo "Error: case file not found: $case_file"
  exit 1
fi

exec ruby harness/runner.rb "$case_file" "$@"
