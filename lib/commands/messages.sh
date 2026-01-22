#!/usr/bin/env bash
# messages.sh - Message board commands


cmd_messages() {
  local action="${1:-list}"

  # Check if first arg is a flag
  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _messages_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _messages_list "$@" ;;
    show) _messages_show "$@" ;;
    create) _messages_create "$@" ;;
    pin) _messages_pin "$@" ;;
    unpin) _messages_unpin "$@" ;;
    update) _messages_update "$@" ;;
    --help|-h) _help_messages ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _messages_show "$action" "$@"
      else
        die "Unknown messages action: $action" $EXIT_USAGE "Run: bcq messages --help"
      fi
      ;;
  esac
}

_messages_list() {
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_messages
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get message board ID from project dock
  local project_data message_board_id
  project_data=$(api_get "/projects/$project.json")
  message_board_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "message_board") | .id // empty')

  if [[ -z "$message_board_id" ]]; then
    die "No message board found in project $project" $EXIT_NOT_FOUND
  fi

  local response
  response=$(api_get "/buckets/$project/message_boards/$message_board_id/messages.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count messages"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq show message <id> --in $project" "Show message details")" \
    "$(breadcrumb "post" "bcq message \"subject\" --in $project" "Post new message")"
  )

  output "$response" "$summary" "$bcs" "_messages_list_md"
}

_messages_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Messages ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No messages found*"
  else
    echo "| # | Subject | Author | Posted |"
    echo "|---|---------|--------|--------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.subject // .title | .[0:40]) | \(.creator.name) | \(.created_at | split("T")[0]) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_messages_show() {
  local message_id="$1"
  shift || true
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        project="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  if [[ -z "$message_id" ]]; then
    die "Message ID required" $EXIT_USAGE "Usage: bcq messages show <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/messages/$message_id.json")

  local subject
  subject=$(echo "$response" | jq -r '.subject // .title // "Untitled"')
  local summary="Message: $subject"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "comment" "bcq comment \"text\" --on $message_id" "Add comment")" \
    "$(breadcrumb "list" "bcq messages --in $project" "Back to messages")"
  )

  output "$response" "$summary" "$bcs" "_messages_show_md"
}

_messages_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local subject author created_at content
  subject=$(echo "$data" | jq -r '.subject // .title // "Untitled"')
  author=$(echo "$data" | jq -r '.creator.name')
  created_at=$(echo "$data" | jq -r '.created_at')
  content=$(echo "$data" | jq -r '.content // ""')

  echo "## $subject"
  echo
  md_kv "Author" "$author" "Posted" "$created_at"

  if [[ -n "$content" ]]; then
    echo
    echo "### Content"
    echo "$content" | sed 's/<[^>]*>//g'  # Strip HTML
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_messages_create() {
  local subject="" project="" content="" draft=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_message_create
        return
        ;;
      --subject|-s)
        [[ -z "${2:-}" ]] && die "--subject requires a value" $EXIT_USAGE
        subject="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      --draft)
        draft=true
        shift
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq message --help"
        ;;
      *)
        die "Unexpected argument: $1" $EXIT_USAGE "Run: bcq message --help"
        ;;
    esac
  done

  if [[ -z "$subject" ]]; then
    die "Message subject required" $EXIT_USAGE "Usage: bcq message --subject \"subject\" --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get message board ID from project dock
  local project_data message_board_id
  project_data=$(api_get "/projects/$project.json")
  message_board_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "message_board") | .id // empty')

  if [[ -z "$message_board_id" ]]; then
    die "No message board found in project $project" $EXIT_NOT_FOUND
  fi

  # Build payload - messages controller has wrap_parameters, so subject/content
  # are auto-wrapped, but status must be top-level
  local payload
  payload=$(jq -n --arg subject "$subject" '{subject: $subject}')

  if [[ -n "$content" ]]; then
    payload=$(echo "$payload" | jq --arg content "$content" '. + {content: $content}')
  fi

  # Default to active (published) status unless --draft is specified
  local status="active"
  if [[ "$draft" == true ]]; then
    status="drafted"
  fi
  payload=$(echo "$payload" | jq --arg s "$status" '. + {status: $s}')

  local response
  response=$(api_post "/buckets/$project/message_boards/$message_board_id/messages.json" "$payload")

  local message_id
  message_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Posted message #$message_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq show message $message_id --in $project" "View message")" \
    "$(breadcrumb "list" "bcq messages --in $project" "List messages")"
  )

  output "$response" "$summary" "$bcs"
}

_messages_update() {
  local message_id="" project="" subject="" content=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --subject|-s)
        [[ -z "${2:-}" ]] && die "--subject requires a value" $EXIT_USAGE
        subject="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$message_id" ]]; then
          message_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$message_id" ]]; then
    die "Message ID required" $EXIT_USAGE "Usage: bcq messages update <id> --subject \"new\" --in <project>"
  fi

  if [[ -z "$subject" ]] && [[ -z "$content" ]]; then
    die "Subject or content required" $EXIT_USAGE "Use --subject and/or --content"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload="{}"
  [[ -n "$subject" ]] && payload=$(echo "$payload" | jq --arg s "$subject" '. + {subject: $s}')
  [[ -n "$content" ]] && payload=$(echo "$payload" | jq --arg c "$content" '. + {content: $c}')

  local response
  response=$(api_put "/buckets/$project/messages/$message_id.json" "$payload")

  local summary="Updated message #$message_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq messages $message_id --in $project" "View message")"
  )

  output "$response" "$summary" "$bcs"
}

_messages_pin() {
  local message_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$message_id" ]]; then
          message_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$message_id" ]]; then
    die "Message ID required" $EXIT_USAGE "Usage: bcq messages pin <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  api_post "/buckets/$project/recordings/$message_id/pin.json" "{}" >/dev/null

  local summary="Pinned message #$message_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "unpin" "bcq messages unpin $message_id --in $project" "Unpin message")" \
    "$(breadcrumb "show" "bcq messages $message_id --in $project" "View message")"
  )

  output '{}' "$summary" "$bcs"
}

_messages_unpin() {
  local message_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$message_id" ]]; then
          message_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$message_id" ]]; then
    die "Message ID required" $EXIT_USAGE "Usage: bcq messages unpin <id> --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  api_delete "/buckets/$project/recordings/$message_id/pin.json" >/dev/null

  local summary="Unpinned message #$message_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "pin" "bcq messages pin $message_id --in $project" "Pin message")" \
    "$(breadcrumb "show" "bcq messages $message_id --in $project" "View message")"
  )

  output '{}' "$summary" "$bcs"
}


_help_message_create() {
  cat << 'EOF'
bcq message - Post a message to a project's message board

USAGE
  bcq message --subject "subject" [options]

OPTIONS
  --subject, -s <text>       Message subject/title (required)
  --content, --body, -b      Message body content
  --in, --project, -p <id>   Project ID or name

EXAMPLES
  bcq message --subject "Weekly Update" --in 123
  bcq message -s "Meeting Notes" --content "Here are the notes..." --in "My Project"
EOF
}

# Shortcut for creating messages
cmd_message() {
  _messages_create "$@"
}
