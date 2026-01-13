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
  local summary="$count ${type,,}s"

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


# Search is an alias for recordings with a note about API limitations
cmd_search() {
  info "Note: Basecamp API doesn't have full-text search."
  info "Using recordings filter instead..."
  info ""
  cmd_recordings "$@"
}
