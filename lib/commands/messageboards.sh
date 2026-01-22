#!/usr/bin/env bash
# messageboards.sh - Message board container resource
# Covers message_boards.md (1 endpoint)

cmd_messageboards() {
  local action="${1:-}"

  case "$action" in
    show) shift; _messageboard_show "$@" ;;
    --help|-h) _help_messageboards ;;
    "")
      _messageboard_show "$@"
      ;;
    -*)
      # Flags go to messageboard show
      _messageboard_show "$@"
      ;;
    *)
      # If numeric, treat as messageboard ID
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _messageboard_show "$@"
      else
        die "Unknown messageboards action: $action" $EXIT_USAGE "Run: bcq messageboards --help"
      fi
      ;;
  esac
}

# Get message board ID from project dock
_get_messageboard_id() {
  local project="$1"
  local dock
  dock=$(api_get "/projects/$project.json" | jq -r '.dock[] | select(.name == "message_board") | .id')
  if [[ -z "$dock" ]] || [[ "$dock" == "null" ]]; then
    die "No message board found for project $project" $EXIT_NOT_FOUND
  fi
  echo "$dock"
}

# GET /buckets/:bucket/message_boards/:message_board.json
_messageboard_show() {
  local project="" messageboard_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --board|-b)
        [[ -z "${2:-}" ]] && die "--board requires a value" $EXIT_USAGE
        messageboard_id="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$messageboard_id" ]]; then
          messageboard_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project <id> or set default"
  fi

  if [[ -z "$messageboard_id" ]]; then
    messageboard_id=$(_get_messageboard_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/message_boards/$messageboard_id.json")

  local messages_count
  messages_count=$(echo "$response" | jq -r '.messages_count // 0')
  local summary="$messages_count messages"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "messages" "bcq messages --project $project" "List all messages")" \
    "$(breadcrumb "project" "bcq projects show $project" "View project details")"
  )

  output "$response" "$summary" "$bcs" "_messageboard_show_md"
}

_messageboard_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title
  title=$(echo "$data" | jq -r '.title // "Message Board"')

  echo "## $title"
  echo
  echo "**$summary**"
  echo
  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "$data" | jq -r '"| Messages | \(.messages_count // 0) |"'
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo
  md_breadcrumbs "$breadcrumbs"
}

_help_messageboards() {
  cat <<'EOF'
## bcq messageboards

View message board container for a project.

### Usage

    bcq messageboards [--project <id>]               Show project's message board
    bcq messageboards show [<id>] [--project <id>]   Show specific message board

### Options

    --project, -p <id>    Project ID (or uses default)
    --board, -b <id>      Message board ID (auto-detected from project if omitted)

### Examples

    # View message board for current project
    bcq messageboards

    # View message board for specific project
    bcq messageboards --project 123

    # View specific message board by ID
    bcq messageboards 456 --project 123

### Notes

A message board is the container that holds all messages in a project. Each
project has exactly one message board in its dock.

EOF
}
