#!/usr/bin/env bash
# extract_impl.sh - Extract implemented API calls from bcq source
#
# Output: JSON array of endpoints with method and path
# Example: [{"method":"GET","path":"/buckets/{bucket}/todos/{id}.json","source":"todos.sh"}]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BCQ_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMMANDS_DIR="$BCQ_ROOT/lib/commands"

if [[ ! -d "$COMMANDS_DIR" ]]; then
  echo "Error: commands directory not found at $COMMANDS_DIR" >&2
  exit 1
fi

# Extract API calls from a single source file
# Patterns:
#   api_get "/buckets/$project/todos/$todo_id.json"
#   api_get_all "/buckets/$project/todolists/$id/todos.json"
#   api_post "/buckets/$project/todos.json" "$payload"
#   api_put "/buckets/$project/todos/$id.json" "$payload"
#   api_delete "/buckets/$project/todos/$id.json"
extract_from_file() {
  local file="$1"
  local filename
  filename=$(basename "$file")

  # Extract api_* calls (including api_get_all)
  grep -E 'api_(get|get_all|post|put|patch|delete)[[:space:]]+"/' "$file" 2>/dev/null | while read -r line; do
    # Extract method and path
    if [[ "$line" =~ api_(get|get_all|post|put|patch|delete)[[:space:]]+\"([^\"]+)\" ]]; then
      local method="${BASH_REMATCH[1]}"
      # Normalize get_all to GET
      [[ "$method" == "get_all" ]] && method="get"
      local path="${BASH_REMATCH[2]}"

      # Uppercase method
      method=$(echo "$method" | tr '[:lower:]' '[:upper:]')

      # Normalize path:
      # 1. Strip query parameters (?foo=bar)
      # 2. Replace bash variables with {id} placeholders
      # /buckets/$project/todos/$id.json?status=... → /buckets/{bucket}/todos/{id}.json
      local normalized
      # Strip everything after ?
      normalized=$(echo "$path" | sed -E 's|\?.*$||')
      # Replace bash variables with {id}
      normalized=$(echo "$normalized" | sed -E 's|\$[a-zA-Z_][a-zA-Z0-9_]*|\{id\}|g')
      # Replace ${var} style variables too
      normalized=$(echo "$normalized" | sed -E 's|\$\{[a-zA-Z_][a-zA-Z0-9_]*\}|\{id\}|g')

      # Replace first {id} with {bucket} for buckets paths
      normalized=$(echo "$normalized" | sed -E 's|/buckets/\{id\}|/buckets/{bucket}|')

      echo "{\"method\":\"$method\",\"path\":\"$normalized\",\"source\":\"$filename\"}"
    fi
  done
}

# Main: output JSON array of all endpoints
echo "["

first=true
for file in "$COMMANDS_DIR"/*.sh; do
  [[ ! -f "$file" ]] && continue

  while IFS= read -r endpoint; do
    [[ -z "$endpoint" ]] && continue

    if [[ "$first" == "true" ]]; then
      first=false
    else
      echo ","
    fi
    echo -n "  $endpoint"
  done < <(extract_from_file "$file")
done

# Also check lib/*.sh for any direct API calls
for file in "$BCQ_ROOT/lib"/*.sh; do
  [[ ! -f "$file" ]] && continue
  [[ "$(basename "$file")" == "api.sh" ]] && continue  # Skip api.sh itself

  while IFS= read -r endpoint; do
    [[ -z "$endpoint" ]] && continue

    if [[ "$first" == "true" ]]; then
      first=false
    else
      echo ","
    fi
    echo -n "  $endpoint"
  done < <(extract_from_file "$file")
done

echo ""
echo "]"
