#!/usr/bin/env bash
# recordings.sh - Cross-project recordings listing
# Provides filtered view of content across projects (workaround for missing search API)


cmd_recordings() {
  local action="${1:-list}"

  # Pass through to _recordings_list for flags or empty
  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _recordings_list "$@"
    return
  fi

  case "$action" in
    list)
      shift || true
      _recordings_list "$@"
      ;;
    trash|trashed)
      shift || true
      _recordings_status "trashed" "$@"
      ;;
    archive|archived)
      shift || true
      _recordings_status "archived" "$@"
      ;;
    restore|active)
      shift || true
      _recordings_status "active" "$@"
      ;;
    visibility|client-visibility)
      shift || true
      _recordings_visibility "$@"
      ;;
    --help|-h)
      _help_recordings
      ;;
    # Type shorthands - pass through to _recordings_list
    todos|todo|messages|message|documents|document|doc|comments|comment|cards|card|uploads|upload)
      _recordings_list "$@"
      ;;
    *)
      die "Unknown recordings action: $action" $EXIT_USAGE "Run: bcq recordings --help"
      ;;
  esac
}


_recordings_list() {
  local type="" project="" status="active" sort="updated_at" direction="desc" limit=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --type|-t)
        [[ -z "${2:-}" ]] && die "--type requires a value" $EXIT_USAGE
        type="$2"
        shift 2
        ;;
      --project|--in|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --status|-s)
        [[ -z "${2:-}" ]] && die "--status requires a value" $EXIT_USAGE
        status="$2"
        shift 2
        ;;
      --sort)
        [[ -z "${2:-}" ]] && die "--sort requires a value" $EXIT_USAGE
        sort="$2"
        shift 2
        ;;
      --direction|--dir)
        [[ -z "${2:-}" ]] && die "--direction requires a value" $EXIT_USAGE
        direction="$2"
        shift 2
        ;;
      --limit|-n)
        [[ -z "${2:-}" ]] && die "--limit requires a value" $EXIT_USAGE
        limit="$2"
        shift 2
        ;;
      --help|-h)
        _help_recordings
        return
        ;;
      *)
        # If looks like a type shorthand
        case "$1" in
          todos|todo) type="Todo"; shift ;;
          messages|message) type="Message"; shift ;;
          documents|document|doc) type="Document"; shift ;;
          comments|comment) type="Comment"; shift ;;
          cards|card) type="Kanban::Card"; shift ;;
          uploads|upload) type="Upload"; shift ;;
          *) shift ;;
        esac
        ;;
    esac
  done

  if [[ -z "$type" ]]; then
    die "Type required. Use --type or shorthand (todos, messages, documents, comments, cards)" $EXIT_USAGE \
      "Example: bcq recordings todos --project 123"
  fi

  # Build query string
  local query="type=$type&status=$status&sort=$sort&direction=$direction"

  if [[ -n "$project" ]]; then
    query="$query&bucket=$project"
  fi

  local response
  response=$(api_get "/projects/recordings.json?$query")

  # Apply client-side limit if specified
  if [[ -n "$limit" ]]; then
    response=$(echo "$response" | jq --argjson limit "$limit" '.[:$limit]')
  fi

  local format
  format=$(get_format)

  local count
  count=$(echo "$response" | jq 'length')
  local type_lower
  type_lower=$(echo "$type" | tr '[:upper:]' '[:lower:]')
  local summary="$count ${type_lower}s"

  if [[ "$format" == "json" ]]; then
    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "show" "bcq show <id> --project <bucket.id>" "Show recording (use bucket.id from result)")"
    )
    json_ok "$response" "$summary" "$bcs"
  else
    echo "## Recordings: $type ($summary)"
    echo
    _recordings_table "$response" "$type"
  fi
}


_recordings_status() {
  local new_status="$1"
  shift || true
  local recording_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$recording_id" ]]; then
          recording_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$recording_id" ]]; then
    die "Recording ID required" $EXIT_USAGE "Usage: bcq recordings $new_status <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_put "/buckets/$project/recordings/$recording_id/status/$new_status.json" "{}")

  # Handle 204 No Content (empty response)
  if [[ -z "$response" ]]; then
    response='{}'
  fi

  local status_msg
  case "$new_status" in
    trashed) status_msg="Trashed" ;;
    archived) status_msg="Archived" ;;
    active) status_msg="Restored" ;;
    *) status_msg="Changed to $new_status" ;;
  esac

  local summary="$status_msg recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq show $recording_id --in $project" "View recording")"
  )

  output "$response" "$summary" "$bcs"
}


# PUT /buckets/:id/recordings/:id/client_visibility.json
_recordings_visibility() {
  local recording_id="" project="" visible=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --visible|--show)
        visible="true"
        shift
        ;;
      --hidden|--hide)
        visible="false"
        shift
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$recording_id" ]]; then
          recording_id="$1"
        elif [[ "$1" == "true" ]] || [[ "$1" == "false" ]]; then
          visible="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$recording_id" ]]; then
    die "Recording ID required" $EXIT_USAGE "Usage: bcq recordings visibility <id> --visible|--hidden --in <project>"
  fi

  if [[ -z "$visible" ]]; then
    die "Visibility required" $EXIT_USAGE "Use --visible or --hidden"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --argjson visible "$visible" '{visible_to_clients: $visible}')

  local response
  response=$(api_put "/buckets/$project/recordings/$recording_id/client_visibility.json" "$payload")

  local summary
  if [[ "$visible" == "true" ]]; then
    summary="Recording #$recording_id now visible to clients"
  else
    summary="Recording #$recording_id now hidden from clients"
  fi

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq show $recording_id --in $project" "View recording")"
  )

  output "$response" "$summary" "$bcs"
}


_recordings_table() {
  local data="$1"
  local type="$2"

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No recordings found*"
    return
  fi

  case "$type" in
    Todo)
      echo "| # | Content | Project | Updated |"
      echo "|---|---------|---------|---------|"
      echo "$data" | jq -r '.[] | "| \(.id) | \(.title // .content | gsub("\n"; " ") | .[0:40]) | \(.bucket.name | .[0:20]) | \(.updated_at | .[0:10]) |"'
      ;;
    Message)
      echo "| # | Subject | Project | Updated |"
      echo "|---|---------|---------|---------|"
      echo "$data" | jq -r '.[] | "| \(.id) | \(.subject // .title | gsub("\n"; " ") | .[0:40]) | \(.bucket.name | .[0:20]) | \(.updated_at | .[0:10]) |"'
      ;;
    Comment)
      echo "| # | Content | Parent | Updated |"
      echo "|---|---------|--------|---------|"
      echo "$data" | jq -r '.[] | "| \(.id) | \(.content | gsub("\n"; " ") | gsub("<[^>]*>"; "") | .[0:40]) | \(.parent.type // "-") | \(.updated_at | .[0:10]) |"'
      ;;
    *)
      echo "| # | Title | Project | Updated |"
      echo "|---|-------|---------|---------|"
      echo "$data" | jq -r '.[] | "| \(.id) | \(.title // .content // .name | gsub("\n"; " ") | .[0:40]) | \(.bucket.name // "-" | .[0:20]) | \(.updated_at | .[0:10]) |"'
      ;;
  esac
}


# Real search using /search.json endpoint
cmd_search() {
  local action="${1:-}"

  # Handle metadata subcommand
  if [[ "$action" == "metadata" ]] || [[ "$action" == "types" ]]; then
    shift || true
    _search_metadata "$@"
    return
  fi
  local query="" type="" project="" creator="" limit=""

  # First positional arg is the query if not a flag
  if [[ $# -gt 0 ]] && [[ ! "$1" =~ ^- ]]; then
    query="$1"
    shift
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --type|-t)
        [[ -z "${2:-}" ]] && die "--type requires a value" $EXIT_USAGE
        type="$2"
        shift 2
        ;;
      --project|--in|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --creator|-c)
        [[ -z "${2:-}" ]] && die "--creator requires a value" $EXIT_USAGE
        creator="$2"
        shift 2
        ;;
      --limit|-n)
        [[ -z "${2:-}" ]] && die "--limit requires a value" $EXIT_USAGE
        limit="$2"
        shift 2
        ;;
      --help|-h)
        _help_search
        return
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq search --help"
        ;;
      *)
        # Remaining positional is query
        if [[ -z "$query" ]]; then
          query="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$query" ]]; then
    die "Search query required" $EXIT_USAGE "Usage: bcq search <query> [--type <type>] [--project <id>]"
  fi

  # Build query string
  local qs="q=$(urlencode "$query")"
  [[ -n "$type" ]] && qs="$qs&type=$type"
  [[ -n "$project" ]] && qs="$qs&bucket_id=$project"
  [[ -n "$creator" ]] && qs="$qs&creator_id=$creator"

  local response
  response=$(api_get "/search.json?$qs")

  # Apply client-side limit if specified
  if [[ -n "$limit" ]]; then
    response=$(echo "$response" | jq --argjson limit "$limit" '.[:$limit]')
  fi

  local format
  format=$(get_format)

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count results for \"$query\""

  if [[ "$format" == "json" ]]; then
    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "show" "bcq show <id> --project <bucket.id>" "Show result details")"
    )
    json_ok "$response" "$summary" "$bcs"
  else
    echo "## Search: $query ($count results)"
    echo
    _search_table "$response"
  fi
}

_search_table() {
  local data="$1"

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No results found*"
    return
  fi

  echo "| # | Type | Title | Project |"
  echo "|---|------|-------|---------|"
  echo "$data" | jq -r '.[] | "| \(.id) | \(.type) | \(.title // .plain_text_content // "" | gsub("<[^>]*>"; "") | gsub("\n"; " ") | .[0:40]) | \(.bucket.name // "-" | .[0:20]) |"'
}

_search_metadata() {
  local response
  response=$(api_get "/searches/metadata.json")

  # Handle empty response (204 No Content)
  if [[ -z "$response" ]] || [[ "$response" == "null" ]]; then
    die "Search metadata not available" $EXIT_NOT_FOUND \
      "Common types: Todo, Message, Document, Comment, Kanban::Card"
  fi

  local format
  format=$(get_format)

  local types file_types
  types=$(echo "$response" | jq -r '[.recording_search_types[].key] | join(", ") // empty')
  file_types=$(echo "$response" | jq -r '[.file_search_types[].key] | join(", ") // empty')

  if [[ "$format" == "json" ]]; then
    json_ok "$response" "Search metadata"
  else
    echo "**--type**: ${types:-Todo, Message, Document, Comment, Kanban::Card}"
    [[ -n "$file_types" ]] && echo "**--file-type** (attachments): $file_types"
  fi
}

_help_search() {
  cat <<'EOF'
## bcq search

Search across all Basecamp content.

### Usage

    bcq search <query> [options]
    bcq search metadata          Show available types

### Options

    --type, -t <type>     Filter by recording type (run `bcq search metadata` for valid types)
    --project, -p <id>    Filter by project ID
    --creator, -c <id>    Filter by creator person ID
    --limit, -n <num>     Limit results

### Examples

    # Search for authentication-related items
    bcq search "authentication"

    # Search todos only
    bcq search "bug fix" --type Todo

    # Search in a specific project
    bcq search "deploy" --project 12345

    # Discover available types
    bcq search metadata

EOF
}
