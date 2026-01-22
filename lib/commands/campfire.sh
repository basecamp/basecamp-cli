#!/usr/bin/env bash
# campfire.sh - Campfire (chat) commands

cmd_campfire() {
  local action="${1:-}"

  # If first arg is a number, treat as campfire ID
  if [[ "$action" =~ ^[0-9]+$ ]]; then
    local campfire_id="$action"
    shift
    action="${1:-messages}"
    shift || true
    _campfire_dispatch "$campfire_id" "$action" "$@"
    return
  fi

  case "$action" in
    delete) _campfire_line_delete "${@:2}" ;;
    line|show) _campfire_line_show "${@:2}" ;;
    list) _campfires_list "${@:2}" ;;
    messages) _campfire_messages "${@:2}" ;;
    post) _campfire_post "${@:2}" ;;
    --help|-h) _help_campfire ;;
    *)
      die "Unknown campfire action: $action" $EXIT_USAGE "Run: bcq campfire --help"
      ;;
  esac
}

_campfire_dispatch() {
  local campfire_id="$1"
  local action="$2"
  shift 2

  case "$action" in
    delete) _campfire_line_delete --campfire "$campfire_id" "$@" ;;
    line|show) _campfire_line_show --campfire "$campfire_id" "$@" ;;
    messages|"") _campfire_messages --campfire "$campfire_id" "$@" ;;
    post) _campfire_post --campfire "$campfire_id" "$@" ;;
    *) die "Unknown campfire action: $action" $EXIT_USAGE ;;
  esac
}

_campfires_list() {
  local project="" all=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --all|-a)
        all="true"
        shift
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  # Account-wide campfire listing
  if [[ "$all" == "true" ]]; then
    local response
    response=$(api_get "/chats.json")

    local count
    count=$(echo "$response" | jq 'length')
    local summary="$count campfires"

    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "messages" "bcq campfire <id> messages --in <project>" "View messages")" \
      "$(breadcrumb "post" "bcq campfire <id> post \"message\" --in <project>" "Post message")"
    )

    output "$response" "$summary" "$bcs" "_campfires_list_md"
    return
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}" "Use --in <project>, --all, or set in .basecamp/config.json")

  # Get campfire from project dock
  local project_data campfire_id campfire_data
  project_data=$(api_get "/projects/$project.json")
  campfire_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "chat") | .id // empty')

  if [[ -z "$campfire_id" ]]; then
    die "No campfire found in project $project" $EXIT_NOT_FOUND
  fi

  # Get campfire details
  campfire_data=$(api_get "/buckets/$project/chats/$campfire_id.json")

  local title
  title=$(echo "$campfire_data" | jq -r '.title // "Campfire"')
  local summary="Campfire: $title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "messages" "bcq campfire $campfire_id messages --in $project" "View messages")" \
    "$(breadcrumb "post" "bcq campfire $campfire_id post \"message\" --in $project" "Post message")"
  )

  # Return as array for consistency
  local result
  result=$(echo "$campfire_data" | jq '[.]')

  output "$result" "$summary" "$bcs" "_campfires_list_md"
}

_campfires_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Campfires ($summary)"
  echo

  # Check if any item has bucket info (account-wide listing)
  local has_bucket
  has_bucket=$(echo "$data" | jq -r '.[0].bucket.name // empty')

  if [[ -n "$has_bucket" ]]; then
    echo "| # | Title | Project | Lines |"
    echo "|---|-------|---------|-------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title // "Campfire") | \(.bucket.name // "-" | .[0:20]) | \(.lines_count // 0) |"'
  else
    echo "| # | Title | Lines |"
    echo "|---|-------|-------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title // "Campfire") | \(.lines_count // 0) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_campfire_messages() {
  local campfire_id="" project="" limit="25"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --campfire|-c)
        [[ -z "${2:-}" ]] && die "--campfire requires a value" $EXIT_USAGE
        campfire_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --limit|-n)
        [[ -z "${2:-}" ]] && die "--limit requires a value" $EXIT_USAGE
        limit="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get campfire ID from project if not specified
  if [[ -z "$campfire_id" ]]; then
    local project_data
    project_data=$(api_get "/projects/$project.json")
    campfire_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "chat") | .id // empty')
  fi

  if [[ -z "$campfire_id" ]]; then
    die "No campfire found" $EXIT_NOT_FOUND
  fi

  # Get recent messages (lines)
  local response
  response=$(api_get "/buckets/$project/chats/$campfire_id/lines.json")

  # Take last N messages
  local messages
  messages=$(echo "$response" | jq --argjson limit "$limit" '.[-$limit:]')

  local count
  count=$(echo "$messages" | jq 'length')
  local summary="$count messages"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "post" "bcq campfire $campfire_id post \"message\"" "Post message")" \
    "$(breadcrumb "more" "bcq campfire $campfire_id messages --limit 50" "Load more")"
  )

  output "$messages" "$summary" "$bcs" "_campfire_messages_md"
}

_campfire_messages_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Campfire ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No messages*"
  else
    # Display messages in chat format
    echo "$data" | jq -r '.[] | "**\(.creator.name)** _\(.created_at | split("T")[0])_\n\(.content | gsub("<[^>]*>"; ""))\n"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_campfire_post() {
  local content="" campfire_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --campfire|-c)
        [[ -z "${2:-}" ]] && die "--campfire requires a value" $EXIT_USAGE
        campfire_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$content" ]]; then
          content="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$content" ]]; then
    die "Message content required" $EXIT_USAGE \
      "Usage: bcq campfire post \"message\" [--in project]"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get campfire ID from project if not specified
  if [[ -z "$campfire_id" ]]; then
    local project_data
    project_data=$(api_get "/projects/$project.json")
    campfire_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "chat") | .id // empty')
  fi

  if [[ -z "$campfire_id" ]]; then
    die "No campfire found in project" $EXIT_NOT_FOUND
  fi

  # Post message
  local payload
  payload=$(jq -n --arg content "$content" '{content: $content}')

  local response
  response=$(api_post "/buckets/$project/chats/$campfire_id/lines.json" "$payload")

  local line_id
  line_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Posted message #$line_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "messages" "bcq campfire $campfire_id messages" "View messages")" \
    "$(breadcrumb "post" "bcq campfire $campfire_id post \"reply\"" "Post another")"
  )

  output "$response" "$summary" "$bcs"
}

_campfire_line_show() {
  local line_id="" campfire_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --campfire|-c)
        [[ -z "${2:-}" ]] && die "--campfire requires a value" $EXIT_USAGE
        campfire_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$line_id" ]]; then
          line_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$line_id" ]]; then
    die "Line ID required" $EXIT_USAGE "Usage: bcq campfire line <id> --campfire <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$campfire_id" ]]; then
    local project_data
    project_data=$(api_get "/projects/$project.json")
    campfire_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "chat") | .id // empty')
  fi

  if [[ -z "$campfire_id" ]]; then
    die "No campfire found" $EXIT_NOT_FOUND
  fi

  local response
  response=$(api_get "/buckets/$project/chats/$campfire_id/lines/$line_id.json")

  local creator
  creator=$(echo "$response" | jq -r '.creator.name // "Unknown"')
  local summary="Line #$line_id by $creator"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "delete" "bcq campfire delete $line_id --campfire $campfire_id --in $project" "Delete line")" \
    "$(breadcrumb "messages" "bcq campfire $campfire_id messages --in $project" "Back to messages")"
  )

  output "$response" "$summary" "$bcs"
}

_campfire_line_delete() {
  local line_id="" campfire_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --campfire|-c)
        [[ -z "${2:-}" ]] && die "--campfire requires a value" $EXIT_USAGE
        campfire_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$line_id" ]]; then
          line_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$line_id" ]]; then
    die "Line ID required" $EXIT_USAGE "Usage: bcq campfire delete <id> --campfire <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$campfire_id" ]]; then
    local project_data
    project_data=$(api_get "/projects/$project.json")
    campfire_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "chat") | .id // empty')
  fi

  if [[ -z "$campfire_id" ]]; then
    die "No campfire found" $EXIT_NOT_FOUND
  fi

  api_delete "/buckets/$project/chats/$campfire_id/lines/$line_id.json" >/dev/null

  local summary="Deleted line #$line_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "messages" "bcq campfire $campfire_id messages --in $project" "Back to messages")"
  )

  output '{}' "$summary" "$bcs"
}

_help_campfire() {
  cat <<'EOF'
## bcq campfire

Interact with Campfire (real-time chat).

### Usage

    bcq campfire <action> [options]
    bcq campfire <id> post "message"    # Post to specific campfire
    bcq campfire <id> messages          # View messages

### Actions

    list              List campfires (in project or account-wide with --all)
    messages          View recent messages
    post "message"    Post a message

### Options

    --all, -a               List all campfires across account
    --in, -p <project>      Project ID
    --campfire, -c <id>     Campfire ID
    --limit, -n <count>     Number of messages (default: 25)

### Examples

    # List all campfires across account
    bcq campfire list --all

    # List campfire in project
    bcq campfire list --in 12345

    # View recent messages
    bcq campfire messages --in 12345
    bcq campfire 67890 messages

    # Post a message
    bcq campfire post "Hello team!" --in 12345
    bcq campfire 67890 post "Status update: done with feature"

    # Thought stream pattern (narrate work)
    bcq campfire post "[10:30] Starting security triage. 7 reports in inbox."

EOF
}
