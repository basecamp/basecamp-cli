#!/usr/bin/env bash
# extract_docs.sh - Extract documented endpoints from bc3-api
#
# Output: JSON array of endpoints with method, path, and source file
# Example: [{"method":"GET","path":"/buckets/{bucket}/todos/{id}.json","source":"todos.md"}]
#
# By default, fetches from GitHub. Use --local to use a local clone instead.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXCLUSIONS_FILE="$SCRIPT_DIR/exclusions.txt"

# GitHub raw content base URL
GITHUB_RAW="https://raw.githubusercontent.com/basecamp/bc3-api/master/sections"
GITHUB_API="https://api.github.com/repos/basecamp/bc3-api/contents/sections"

# Cache directory (speeds up repeated runs)
CACHE_DIR="${BCQ_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/bcq}/api_docs"
CACHE_TTL=3600  # 1 hour

# Load exclusions into a string for pattern matching
EXCLUSIONS=""
if [[ -f "$EXCLUSIONS_FILE" ]]; then
  while IFS= read -r line; do
    [[ "$line" =~ ^#.*$ || -z "$line" ]] && continue
    EXCLUSIONS="$EXCLUSIONS|$line"
  done < "$EXCLUSIONS_FILE"
  EXCLUSIONS="${EXCLUSIONS#|}"  # Remove leading |
fi

is_excluded() {
  local filename="${1%.md}"  # Remove .md extension
  [[ -n "$EXCLUSIONS" ]] && [[ "$filename" =~ ^($EXCLUSIONS)$ ]]
}

# Extract endpoints from markdown content - outputs to stdout
extract_from_content() {
  local filename="$1"
  local content="$2"

  is_excluded "$filename" && return 0

  # Pattern: * `METHOD /path` - store in variable for bash version compatibility
  local endpoint_pattern='^[*] `([A-Z]+) ([^`]+)`'

  # Use process substitution to avoid subshell (pipeline would lose output)
  while IFS= read -r line; do
    if [[ "$line" =~ $endpoint_pattern ]]; then
      local method="${BASH_REMATCH[1]}"
      local path="${BASH_REMATCH[2]}"

      # Normalize path
      local normalized="$path"
      normalized=$(echo "$normalized" | sed -E 's|/([0-9]+)|/{id}|g')
      normalized=$(echo "$normalized" | sed -E 's|:[a-z_]+_id|{id}|g')
      normalized=$(echo "$normalized" | sed -E 's|:[a-z_]+|{id}|g')
      normalized=$(echo "$normalized" | sed -E 's|/buckets/\{id\}|/buckets/{bucket}|')

      printf '%s\n' "{\"method\":\"$method\",\"path\":\"$normalized\",\"source\":\"$filename\"}"
    fi
  done < <(echo "$content" | grep -E '^\* `(GET|POST|PUT|PATCH|DELETE) /' 2>/dev/null || true)
}

# Process all files and output JSON
process_files() {
  local source="$1"  # "local" or "github"
  local files=()
  local contents=()

  if [[ "$source" == "local" ]]; then
    local bc3_dir="${BC3_API_DIR:-$HOME/Work/basecamp/bc3-api}"
    local sections_dir="$bc3_dir/sections"

    if [[ ! -d "$sections_dir" ]]; then
      echo "Error: bc3-api not found at $sections_dir" >&2
      echo "Set BC3_API_DIR or omit --local to fetch from GitHub" >&2
      exit 1
    fi

    for file in "$sections_dir"/*.md; do
      [[ -f "$file" ]] || continue
      local filename
      filename=$(basename "$file")
      is_excluded "$filename" && continue

      # Extract directly
      extract_from_content "$filename" "$(cat "$file")"
    done
  else
    # GitHub fetch
    mkdir -p "$CACHE_DIR"

    local files_cache="$CACHE_DIR/files.json"
    local now
    now=$(date +%s)

    # Check cache freshness
    if [[ -f "$files_cache" ]]; then
      local cache_time
      cache_time=$(stat -f %m "$files_cache" 2>/dev/null || stat -c %Y "$files_cache" 2>/dev/null || echo 0)
      if (( now - cache_time >= CACHE_TTL )); then
        rm -f "$files_cache"
      fi
    fi

    # Fetch file list if not cached
    if [[ ! -f "$files_cache" ]]; then
      if ! curl -sfL "$GITHUB_API" -o "$files_cache" 2>/dev/null; then
        echo "Error: Failed to fetch file list from GitHub" >&2
        echo "Check network connection or use --local with BC3_API_DIR" >&2
        exit 1
      fi
    fi

    # Parse file list
    local filenames
    filenames=$(jq -r '.[] | select(.name | endswith(".md")) | .name' "$files_cache" 2>/dev/null)

    if [[ -z "$filenames" ]]; then
      echo "Error: No markdown files found in bc3-api sections" >&2
      exit 1
    fi

    for filename in $filenames; do
      is_excluded "$filename" && continue

      local content_cache="$CACHE_DIR/$filename"

      # Check content cache
      if [[ -f "$content_cache" ]]; then
        local cache_time
        cache_time=$(stat -f %m "$content_cache" 2>/dev/null || stat -c %Y "$content_cache" 2>/dev/null || echo 0)
        if (( now - cache_time >= CACHE_TTL )); then
          rm -f "$content_cache"
        fi
      fi

      # Fetch if not cached
      if [[ ! -f "$content_cache" ]]; then
        curl -sfL "$GITHUB_RAW/$filename" -o "$content_cache" 2>/dev/null || continue
      fi

      extract_from_content "$filename" "$(cat "$content_cache")"
    done
  fi
}

# Main
use_local=false
for arg in "$@"; do
  case "$arg" in
    --local) use_local=true ;;
    --help|-h)
      echo "Usage: extract_docs.sh [--local]"
      echo ""
      echo "Extract API endpoints from bc3-api documentation."
      echo ""
      echo "Options:"
      echo "  --local    Use local bc3-api clone instead of GitHub"
      echo "             Set BC3_API_DIR to specify location"
      echo ""
      echo "Output: JSON array of endpoints"
      exit 0
      ;;
  esac
done

# Collect endpoints
if [[ "$use_local" == "true" ]]; then
  source_type="local"
else
  source_type="github"
fi

# Process and format as JSON array
echo "["
first=true
while IFS= read -r endpoint; do
  [[ -z "$endpoint" ]] && continue
  if [[ "$first" == "true" ]]; then
    first=false
  else
    echo ","
  fi
  printf '  %s' "$endpoint"
done < <(process_files "$source_type")
printf '\n'
echo "]"
