#!/usr/bin/env bash
# Run eval case with an LLM agent
# Usage: ./run-agent.sh <case> <prompt> [--model MODEL]

set -euo pipefail
cd "$(dirname "$0")"

# Load API keys
if [[ -f "../.env" ]]; then
  source "../.env"
fi

case_name="${1:-}"
prompt_name="${2:-api-docs-with-agent-invariants}"
shift 2 || true

if [[ -z "$case_name" ]]; then
  echo "Usage: ./run-agent.sh <case> [prompt] [--model MODEL]"
  echo ""
  echo "Available cases:"
  for f in cases/*.yml; do
    name=$(basename "$f" .yml)
    echo "  $name"
  done
  echo ""
  echo "Available prompts:"
  for f in ../prompts/*.md; do
    name=$(basename "$f" .md)
    echo "  $name"
  done
  exit 1
fi

exec ruby harness/agent_runner.rb "$case_name" "$prompt_name" "$@"
