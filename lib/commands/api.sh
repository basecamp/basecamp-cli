#!/usr/bin/env bash
# api.sh - Raw API access commands

cmd_api() {
  local action="${1:-}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _help_api
    return
  fi

  shift || true

  case "$action" in
    get)     _api_get "$@" ;;
    post)    _api_post "$@" ;;
    put)     _api_put "$@" ;;
    delete)  _api_delete "$@" ;;
    --help|-h) _help_api ;;
    *)
      die "Unknown api action: $action" $EXIT_USAGE "Actions: get, post, put, delete"
      ;;
  esac
}

# Parse path from various formats
_api_parse_path() {
  local path="$1"

  # Extract path from full URL if provided
  # Handles: https://3.basecampapi.com/12345/projects.json
  if [[ "$path" =~ ^https?://[^/]+/[0-9]+(/.*) ]]; then
    path="${BASH_REMATCH[1]}"
  fi

  # Ensure leading slash
  [[ "$path" != /* ]] && path="/$path"

  echo "$path"
}

# Generate breadcrumbs based on path
_api_breadcrumbs() {
  local path="$1"
  local bcs=""

  if [[ "$path" == */projects.json ]]; then
    bcs=$(breadcrumbs \
      "$(breadcrumb "details" "bcq api get /projects/<id>.json" "Get project details")" \
      "$(breadcrumb "list" "bcq projects" "List projects with formatting")"
    )
  elif [[ "$path" =~ /buckets/([0-9]+)/card_tables/([0-9]+)\.json ]]; then
    local bucket="${BASH_REMATCH[1]}"
    bcs=$(breadcrumbs \
      "$(breadcrumb "cards" "bcq cards --in $bucket" "List cards")" \
      "$(breadcrumb "columns" "bcq cards columns --in $bucket" "List columns")"
    )
  elif [[ "$path" =~ /buckets/([0-9]+) ]]; then
    local bucket="${BASH_REMATCH[1]}"
    bcs=$(breadcrumbs \
      "$(breadcrumb "project" "bcq api get /projects/$bucket.json" "Get project details")" \
      "$(breadcrumb "todos" "bcq todos --in $bucket" "List todos")" \
      "$(breadcrumb "cards" "bcq cards --in $bucket" "List cards")"
    )
  fi

  echo "$bcs"
}

# Generate summary from response
_api_summary() {
  local response="$1"
  local summary=""

  # Check if array response
  if echo "$response" | jq -e 'type == "array"' >/dev/null 2>&1; then
    local count
    count=$(echo "$response" | jq 'length')
    summary="$count items"
  else
    # Single object - try to get type/title
    local item_type title
    item_type=$(echo "$response" | jq -r '.type // ""')
    title=$(echo "$response" | jq -r '.title // .name // .subject // ""' | head -c 50)
    if [[ -n "$item_type" ]] && [[ -n "$title" ]]; then
      summary="$item_type: $title"
    elif [[ -n "$item_type" ]]; then
      summary="$item_type"
    elif [[ -n "$title" ]]; then
      summary="$title"
    else
      summary="API response"
    fi
  fi

  echo "$summary"
}

_api_get() {
  local path=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_api_get
        return
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq api get --help"
        ;;
      *)
        if [[ -z "$path" ]]; then
          path="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$path" ]]; then
    die "API path required" $EXIT_USAGE "Usage: bcq api get <path>"
  fi

  path=$(_api_parse_path "$path")

  local response
  response=$(api_get "$path")

  local summary bcs
  summary=$(_api_summary "$response")
  bcs=$(_api_breadcrumbs "$path")

  output "$response" "$summary" "$bcs"
}

_api_post() {
  local path="" data=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_api_post
        return
        ;;
      --data|-d)
        [[ -z "${2:-}" ]] && die "--data requires a value" $EXIT_USAGE
        data="$2"
        shift 2
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq api post --help"
        ;;
      *)
        if [[ -z "$path" ]]; then
          path="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$path" ]]; then
    die "API path required" $EXIT_USAGE "Usage: bcq api post <path> --data '{...}'"
  fi

  if [[ -z "$data" ]]; then
    die "--data required" $EXIT_USAGE "Usage: bcq api post <path> --data '{...}'"
  fi

  path=$(_api_parse_path "$path")

  local response
  response=$(api_post "$path" "$data")

  local summary
  summary=$(_api_summary "$response")

  output "$response" "✓ POST $path: $summary"
}

_api_put() {
  local path="" data=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_api_put
        return
        ;;
      --data|-d)
        [[ -z "${2:-}" ]] && die "--data requires a value" $EXIT_USAGE
        data="$2"
        shift 2
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq api put --help"
        ;;
      *)
        if [[ -z "$path" ]]; then
          path="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$path" ]]; then
    die "API path required" $EXIT_USAGE "Usage: bcq api put <path> --data '{...}'"
  fi

  if [[ -z "$data" ]]; then
    die "--data required" $EXIT_USAGE "Usage: bcq api put <path> --data '{...}'"
  fi

  path=$(_api_parse_path "$path")

  local response
  response=$(api_put "$path" "$data")

  local summary
  summary=$(_api_summary "$response")

  output "$response" "✓ PUT $path: $summary"
}

_api_delete() {
  local path=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_api_delete
        return
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq api delete --help"
        ;;
      *)
        if [[ -z "$path" ]]; then
          path="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$path" ]]; then
    die "API path required" $EXIT_USAGE "Usage: bcq api delete <path>"
  fi

  path=$(_api_parse_path "$path")

  local response
  response=$(api_delete "$path" 2>/dev/null || echo '{}')

  output "$response" "✓ DELETE $path"
}

_help_api() {
  cat <<'EOF'
## bcq api

Raw API access for any Basecamp endpoint.

### Usage

    bcq api <verb> <path> [options]

### Verbs

    get      GET request (read data)
    post     POST request (create) - requires --data
    put      PUT request (update) - requires --data
    delete   DELETE request (remove)

### Path Formats

    /projects.json                           Relative path with leading slash
    projects.json                            Auto-adds leading slash
    https://3.basecampapi.com/123/path.json  Extracts path from full URL

### Examples

    # Read operations
    bcq api get /projects.json
    bcq api get /buckets/123/card_tables/456.json

    # Create operations
    bcq api post /buckets/123/todolists/456/todos.json --data '{"content":"New todo"}'

    # Update operations
    bcq api put /buckets/123/todos/789.json --data '{"content":"Updated"}'

    # Delete operations
    bcq api delete /buckets/123/todos/789.json

### Notes

Uses your authenticated session. For common operations, prefer dedicated commands
(bcq projects, bcq cards, etc.) which provide better formatting and error handling.

EOF
}

_help_api_get() {
  cat <<'EOF'
## bcq api get

Raw GET request to any Basecamp endpoint.

### Usage

    bcq api get <path>

### Examples

    bcq api get /projects.json
    bcq api get /projects/12345.json
    bcq api get /buckets/12345/card_tables/67890.json

    # From full URL (extracts path automatically)
    bcq api get "https://3.basecampapi.com/123/buckets/456/todos/789.json"

EOF
}

_help_api_post() {
  cat <<'EOF'
## bcq api post

Raw POST request to any Basecamp endpoint.

### Usage

    bcq api post <path> --data '<json>'

### Examples

    # Create a todo
    bcq api post /buckets/123/todolists/456/todos.json \
      --data '{"content":"New todo","assignee_ids":[789]}'

    # Create a card
    bcq api post /buckets/123/card_tables/lists/456/cards.json \
      --data '{"title":"New card","content":"Description"}'

EOF
}

_help_api_put() {
  cat <<'EOF'
## bcq api put

Raw PUT request to any Basecamp endpoint.

### Usage

    bcq api put <path> --data '<json>'

### Examples

    # Update a todo
    bcq api put /buckets/123/todos/456.json \
      --data '{"content":"Updated content"}'

    # Update a card
    bcq api put /buckets/123/card_tables/cards/456.json \
      --data '{"title":"New title"}'

EOF
}

_help_api_delete() {
  cat <<'EOF'
## bcq api delete

Raw DELETE request to any Basecamp endpoint.

### Usage

    bcq api delete <path>

### Examples

    # Delete (trash) a todo
    bcq api delete /buckets/123/todos/456.json

### Notes

Most DELETE operations in Basecamp move items to trash rather than
permanently deleting them. Check the API documentation for specifics.

EOF
}
