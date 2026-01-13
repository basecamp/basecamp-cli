#!/usr/bin/env bash
# forwards.sh - Email forwards (inbox) management
# Covers:
#   - inboxes.md (1 endpoint)
#   - forwards.md (2 endpoints, trash via recordings)
#   - inbox_replies.md (2 endpoints)

cmd_forwards() {
  local action="${1:-}"

  case "$action" in
    list|ls|"") shift || true; _forwards_list "$@" ;;
    show|get) shift; _forward_show "$@" ;;
    inbox) shift; _inbox_show "$@" ;;
    replies) shift; _forward_replies_list "$@" ;;
    reply) shift; _forward_reply_show "$@" ;;
    --help|-h) _help_forwards ;;
    -*)
      # Flags go to list
      _forwards_list "$@"
      ;;
    *)
      # If numeric, treat as forward ID to show
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _forward_show "$@"
      else
        die "Unknown forwards action: $action" $EXIT_USAGE "Run: bcq forwards --help"
      fi
      ;;
  esac
}

# GET /buckets/:bucket/inboxes/:inbox/forwards.json
_forwards_list() {
  local project="" inbox_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --inbox)
        [[ -z "${2:-}" ]] && die "--inbox requires a value" $EXIT_USAGE
        inbox_id="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Get inbox ID from project dock if not provided
  if [[ -z "$inbox_id" ]]; then
    inbox_id=$(_get_project_inbox_id "$project")
    if [[ -z "$inbox_id" ]]; then
      die "Could not find inbox for project" $EXIT_USAGE "Use --inbox to specify inbox ID"
    fi
  fi

  local response
  response=$(api_get "/buckets/$project/inboxes/$inbox_id/forwards.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count forwards"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq forwards show <id> -p $project" "View a forward")" \
    "$(breadcrumb "inbox" "bcq forwards inbox -p $project" "View inbox details")"
  )

  output "$response" "$summary" "$bcs" "_forwards_list_md"
}

_forwards_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Email Forwards ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No forwards found*"
  else
    echo "| ID | Subject | From | Replies |"
    echo "|----|---------|------|---------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.subject | .[0:40]) | \(.from | .[0:25]) | \(.replies_count // 0) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/inbox_forwards/:id.json
_forward_show() {
  local forward_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$forward_id" ]]; then
          forward_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$forward_id" ]]; then
    die "Forward ID required" $EXIT_USAGE "Usage: bcq forwards show <id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/inbox_forwards/$forward_id.json")

  local subject
  subject=$(echo "$response" | jq -r '.subject // "Forward"')
  local summary="$subject"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "replies" "bcq forwards replies $forward_id -p $project" "View replies")" \
    "$(breadcrumb "list" "bcq forwards -p $project" "List all forwards")"
  )

  output "$response" "$summary" "$bcs" "_forward_show_md"
}

_forward_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local subject from replies_count
  subject=$(echo "$data" | jq -r '.subject // "Forward"')
  from=$(echo "$data" | jq -r '.from // "Unknown"')
  replies_count=$(echo "$data" | jq -r '.replies_count // 0')

  echo "## $subject"
  echo

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "| From | $from |"
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo "| Replies | $replies_count |"
  echo

  local content
  content=$(echo "$data" | jq -r '.content // ""')
  if [[ -n "$content" ]] && [[ "$content" != "null" ]]; then
    echo "### Content"
    echo
    echo "$content"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/inboxes/:id.json
_inbox_show() {
  local project="" inbox_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --inbox)
        [[ -z "${2:-}" ]] && die "--inbox requires a value" $EXIT_USAGE
        inbox_id="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$inbox_id" ]]; then
          inbox_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Get inbox ID from project dock if not provided
  if [[ -z "$inbox_id" ]]; then
    inbox_id=$(_get_project_inbox_id "$project")
    if [[ -z "$inbox_id" ]]; then
      die "Could not find inbox for project" $EXIT_USAGE "Use --inbox to specify inbox ID"
    fi
  fi

  local response
  response=$(api_get "/buckets/$project/inboxes/$inbox_id.json")

  local title forwards_count
  title=$(echo "$response" | jq -r '.title // "Inbox"')
  forwards_count=$(echo "$response" | jq -r '.forwards_count // 0')
  local summary="$title ($forwards_count forwards)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "forwards" "bcq forwards -p $project" "List forwards")"
  )

  output "$response" "$summary" "$bcs" "_inbox_show_md"
}

_inbox_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title
  title=$(echo "$data" | jq -r '.title // "Inbox"')

  echo "## $title"
  echo

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "$data" | jq -r '"| Forwards | \(.forwards_count // 0) |"'
  echo "$data" | jq -r '"| Status | \(.status) |"'
  echo "$data" | jq -r '"| Visible to clients | \(.visible_to_clients) |"'
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/inbox_forwards/:forward_id/replies.json
_forward_replies_list() {
  local forward_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$forward_id" ]]; then
          forward_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$forward_id" ]]; then
    die "Forward ID required" $EXIT_USAGE "Usage: bcq forwards replies <forward_id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/inbox_forwards/$forward_id/replies.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count replies to forward #$forward_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "forward" "bcq forwards show $forward_id -p $project" "View the forward")" \
    "$(breadcrumb "reply" "bcq forwards reply $forward_id <reply_id> -p $project" "View a reply")"
  )

  output "$response" "$summary" "$bcs" "_forward_replies_list_md"
}

_forward_replies_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Replies ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No replies found*"
  else
    echo "| ID | Title | By | Date |"
    echo "|----|-------|----|----|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title | .[0:30]) | \(.creator.name // "Unknown") | \(.created_at | split("T")[0]) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/inbox_forwards/:forward_id/replies/:id.json
_forward_reply_show() {
  local forward_id="" reply_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]]; then
          if [[ -z "$forward_id" ]]; then
            forward_id="$1"
          elif [[ -z "$reply_id" ]]; then
            reply_id="$1"
          fi
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$forward_id" ]]; then
    die "Forward ID required" $EXIT_USAGE "Usage: bcq forwards reply <forward_id> <reply_id> --project <id>"
  fi

  if [[ -z "$reply_id" ]]; then
    die "Reply ID required" $EXIT_USAGE "Usage: bcq forwards reply <forward_id> <reply_id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/inbox_forwards/$forward_id/replies/$reply_id.json")

  local title
  title=$(echo "$response" | jq -r '.title // "Reply"')
  local summary="$title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "forward" "bcq forwards show $forward_id -p $project" "View the forward")" \
    "$(breadcrumb "replies" "bcq forwards replies $forward_id -p $project" "List all replies")"
  )

  output "$response" "$summary" "$bcs" "_forward_reply_show_md"
}

_forward_reply_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title creator
  title=$(echo "$data" | jq -r '.title // "Reply"')
  creator=$(echo "$data" | jq -r '.creator.name // "Unknown"')

  echo "## $title"
  echo

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "| By | $creator |"
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo

  local content
  content=$(echo "$data" | jq -r '.content // ""')
  if [[ -n "$content" ]] && [[ "$content" != "null" ]]; then
    echo "### Content"
    echo
    echo "$content"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

# Helper: get inbox ID from project dock
_get_project_inbox_id() {
  local project="$1"
  local project_data
  project_data=$(api_get "/projects/$project.json")
  echo "$project_data" | jq -r '.dock[] | select(.name == "inbox") | .id // empty'
}

_help_forwards() {
  cat <<'EOF'
## bcq forwards

Manage email forwards (inbox).

### Usage

    bcq forwards [options]                       List forwards in project inbox
    bcq forwards show <id> [options]             Show a forward
    bcq forwards inbox [options]                 Show inbox details
    bcq forwards replies <forward_id> [options]  List replies to a forward
    bcq forwards reply <fwd_id> <reply_id>       Show a specific reply

### Options

    --project, -p <id>        Project ID (or set via config)
    --inbox <id>              Inbox ID (auto-detected from project)

### Examples

    # List forwards in project inbox
    bcq forwards -p 123

    # Show a forward
    bcq forwards show 456 -p 123

    # View inbox details
    bcq forwards inbox -p 123

    # List replies to a forward
    bcq forwards replies 456 -p 123

    # Show a specific reply
    bcq forwards reply 456 789 -p 123

### Notes

Forwards are emails forwarded into Basecamp. Each project has an inbox
that can receive forwarded emails. The inbox ID is automatically detected
from the project dock, or can be specified with --inbox.
EOF
}
