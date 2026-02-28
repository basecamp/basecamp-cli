#!/usr/bin/env bash
# Compare CLI surface snapshots and fail on removals.
# Usage: scripts/check-cli-surface-diff.sh <baseline> <current>
set -euo pipefail
BASELINE="$1"
CURRENT="$2"
REMOVED=$(comm -23 "$BASELINE" "$CURRENT")
if [ -n "$REMOVED" ]; then
  echo "FAIL: CLI surface removals detected:"
  echo "$REMOVED"
  echo ""
  echo "If intentional, this is a breaking change."
  exit 1
fi
echo "PASS: no CLI surface removals"
