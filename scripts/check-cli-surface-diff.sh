#!/usr/bin/env bash
# Compare CLI surface snapshots and fail on unacknowledged removals.
# Usage: scripts/check-cli-surface-diff.sh <baseline> <current>
#
# Intentional breaking changes can be listed in .surface-breaking (one per line).
# Clear that file after each release.
set -euo pipefail
BASELINE="$1"
CURRENT="$2"
ALLOWLIST="${3:-.surface-breaking}"

REMOVED=$(LC_ALL=C comm -23 "$BASELINE" "$CURRENT")
if [ -z "$REMOVED" ]; then
  echo "PASS: no CLI surface removals"
  exit 0
fi

# Filter out acknowledged removals
if [ -f "$ALLOWLIST" ]; then
  UNACKED=$(LC_ALL=C comm -23 <(echo "$REMOVED" | sort) <(sort "$ALLOWLIST") || true)
  ACKED=$(LC_ALL=C comm -12 <(echo "$REMOVED" | sort) <(sort "$ALLOWLIST") || true)
  if [ -n "$ACKED" ]; then
    echo "Acknowledged breaking changes:"
    echo "$ACKED" | sed 's/^/  /'
    echo ""
  fi
else
  UNACKED="$REMOVED"
fi

if [ -n "$UNACKED" ]; then
  echo "FAIL: unacknowledged CLI surface removals:"
  echo "$UNACKED"
  echo ""
  echo "If intentional, add to .surface-breaking (one entry per line)."
  exit 1
fi

echo "PASS: all removals acknowledged as breaking changes"
