#!/usr/bin/env bash
# Compare CLI surface snapshots and fail on unacknowledged removals.
# Usage: scripts/check-cli-surface-diff.sh <baseline> <current>
#
# Intentional breaking changes can be listed in .surface-breaking (one per line).
# The allowlist is cumulative: entries stay after release, recording every
# removal acknowledged since the baseline — no release step clears it. Know the
# cost: an entry excuses every future removal of the same surface line, so a
# command removed, later reintroduced, and removed again passes on the old
# acknowledgement. Scoping entries to one release would take a clear-at-release
# step in RELEASING.md, which does not exist today.
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
  UNACKED=$(LC_ALL=C comm -23 <(echo "$REMOVED" | LC_ALL=C sort) <(LC_ALL=C sort "$ALLOWLIST") || true)
  ACKED=$(LC_ALL=C comm -12 <(echo "$REMOVED" | LC_ALL=C sort) <(LC_ALL=C sort "$ALLOWLIST") || true)
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
