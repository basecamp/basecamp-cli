#!/usr/bin/env bash
set -euo pipefail

DOCS_URL="${BCQ_API_DOCS_URL:-https://raw.githubusercontent.com/basecamp/bc3-api/refs/heads/master/README.md}"
CACHE_DIR="${BCQ_API_DOCS_CACHE_DIR:-$HOME/.cache/bcq/api-docs}"
README="$CACHE_DIR/README.md"
LOCAL_README="$HOME/Work/basecamp/bc3-api/README.md"

if [[ -f "$LOCAL_README" ]]; then
  echo "$LOCAL_README"
  exit 0
fi

mkdir -p "$CACHE_DIR"

if [[ -f "$README" ]]; then
  # Update cached copy if remote is newer.
  curl -fsSL -z "$README" -o "$README" "$DOCS_URL" || true
else
  curl -fsSL -o "$README" "$DOCS_URL"
fi

if [[ ! -s "$README" ]]; then
  echo "Failed to fetch Basecamp API docs: $DOCS_URL" >&2
  exit 1
fi

echo "$README"
