#!/usr/bin/env bash
# Generate deterministic CLI surface snapshot from --help --agent output.
# Every line includes the full command path (rooted at "basecamp") to prevent
# cross-command collisions and guarantee traceability.
# Usage: scripts/check-cli-surface.sh [binary] [output-file]
set -euo pipefail

BINARY="${1:-./bin/basecamp}"
OUTPUT="${2:-/dev/stdout}"

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required but not installed. See CONTRIBUTING.md." >&2
  exit 1
fi

walk_commands() {
  local cmd_path="$1"
  local json

  # Build args: root ("basecamp") passes nothing; children pass subcommand names
  local -a args=()
  if [ "$cmd_path" != "basecamp" ]; then
    # shellcheck disable=SC2206 # intentional word-split on space-delimited path
    args=(${cmd_path#basecamp })
  fi

  if ! json=$("$BINARY" "${args[@]}" --help --agent 2>/dev/null); then
    echo "ERROR: failed to get help for: $cmd_path" >&2
    exit 1
  fi

  # Emit: every record carries the full command path to stay unique after sort
  echo "$json" | jq -r --arg path "$cmd_path" '
    "CMD \($path)",
    ((.flags // []) | sort_by(.name) | .[] |
      "FLAG \($path) --\(.name) type=\(.type)"),
    ((.subcommands // []) | sort_by(.name) | .[] |
      "SUB \($path) \(.name)")
  '

  # Recurse into subcommands
  local subs
  subs=$(echo "$json" | jq -r '.subcommands // [] | .[].name')
  for sub in $subs; do
    walk_commands "$cmd_path $sub"
  done
}

walk_commands "basecamp" | LC_ALL=C sort > "$OUTPUT"
