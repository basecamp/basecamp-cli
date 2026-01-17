#!/usr/bin/env bash
set -euo pipefail

DOCS_URL="${BCQ_API_DOCS_URL:-https://raw.githubusercontent.com/basecamp/bc3-api/master/README.md}"
CACHE_DIR="${BCQ_API_DOCS_CACHE_DIR:-$HOME/.cache/bcq/api-docs}"
README="$CACHE_DIR/README.md"
LOCAL_ROOT="$HOME/Work/basecamp/bc3-api"
LOCAL_README="$LOCAL_ROOT/README.md"

mkdir -p "$CACHE_DIR"

fetch_remote() {
  local url="$1" dest="$2"
  mkdir -p "$(dirname "$dest")"
  if [[ -f "$dest" ]]; then
    curl -fsSL -z "$dest" -o "$dest" "$url" || true
  else
    curl -fsSL -o "$dest" "$url"
  fi
  [[ -s "$dest" ]] || { echo "Failed to fetch Basecamp API docs: $url" >&2; exit 1; }
}

get_readme() {
  if [[ -f "$LOCAL_README" ]]; then
    echo "$LOCAL_README"
    return
  fi
  fetch_remote "$DOCS_URL" "$README"
  echo "$README"
}

get_doc() {
  local path="$1"
  path="${path#/}"

  # Prefer local clone if present.
  if [[ -f "$LOCAL_ROOT/$path" ]]; then
    echo "$LOCAL_ROOT/$path"
    return
  fi

  # Fall back to cached remote docs.
  local base_url="${DOCS_URL%/README.md}"
  local dest="$CACHE_DIR/$path"
  fetch_remote "$base_url/$path" "$dest"
  echo "$dest"
}

if [[ $# -gt 0 ]]; then
  get_doc "$1"
else
  get_readme
fi
