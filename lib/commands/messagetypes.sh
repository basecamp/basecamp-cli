#!/usr/bin/env bash
# messagetypes.sh - Message type (category) management
# Covers message_types.md (5 endpoints)
# Note: API uses "categories" path, but UI calls them "message types"

cmd_messagetypes() {
  local action="${1:-}"

  case "$action" in
    list) shift; _messagetypes_list "$@" ;;
    show) shift; _messagetype_show "$@" ;;
    create) shift; _messagetype_create "$@" ;;
    update) shift; _messagetype_update "$@" ;;
    delete) shift; _messagetype_delete "$@" ;;
    --help|-h) _help_messagetypes ;;
    "")
      _messagetypes_list "$@"
      ;;
    -*)
      # Flags go to list
      _messagetypes_list "$@"
      ;;
    *)
      # If numeric, treat as ID to show
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _messagetype_show "$@"
      else
        die "Unknown messagetypes action: $action" $EXIT_USAGE "Run: bcq messagetypes --help"
      fi
      ;;
  esac
}

# GET /buckets/:bucket/categories.json
_messagetypes_list() {
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
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

  local response
  response=$(api_get "/buckets/$project/categories.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count message types"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq messagetypes show <id> -p $project" "View message type")" \
    "$(breadcrumb "create" "bcq messagetypes create \"Name\" --icon \"emoji\" -p $project" "Create message type")"
  )

  output "$response" "$summary" "$bcs" "_messagetypes_list_md"
}

_messagetypes_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Message Types ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No message types found*"
  else
    echo "| ID | Icon | Name |"
    echo "|----|------|------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.icon) | \(.name) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/categories/:id.json
_messagetype_show() {
  local type_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$type_id" ]]; then
          type_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$type_id" ]]; then
    die "Message type ID required" $EXIT_USAGE "Usage: bcq messagetypes show <id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/categories/$type_id.json")

  local name icon
  name=$(echo "$response" | jq -r '.name // "Message Type"')
  icon=$(echo "$response" | jq -r '.icon // ""')
  local summary="$icon $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq messagetypes update $type_id --name \"New Name\" -p $project" "Update message type")" \
    "$(breadcrumb "delete" "bcq messagetypes delete $type_id -p $project" "Delete message type")" \
    "$(breadcrumb "list" "bcq messagetypes -p $project" "List message types")"
  )

  output "$response" "$summary" "$bcs" "_messagetype_show_md"
}

_messagetype_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local name icon
  name=$(echo "$data" | jq -r '.name // "Message Type"')
  icon=$(echo "$data" | jq -r '.icon // ""')

  echo "## $icon $name"
  echo

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "$data" | jq -r '"| Icon | \(.icon) |"'
  echo "$data" | jq -r '"| Name | \(.name) |"'
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo "$data" | jq -r '"| Updated | \(.updated_at | split("T")[0]) |"'
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/categories.json
_messagetype_create() {
  local name="" icon="" project=""

  # First positional arg is name if not a flag
  if [[ $# -gt 0 ]] && [[ ! "$1" =~ ^- ]]; then
    name="$1"
    shift
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --icon)
        [[ -z "${2:-}" ]] && die "--icon requires a value" $EXIT_USAGE
        icon="$2"
        shift 2
        ;;
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ -z "$name" ]] && [[ ! "$1" =~ ^- ]]; then
          name="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$name" ]]; then
    die "Name required" $EXIT_USAGE "Usage: bcq messagetypes create \"Name\" --icon \"emoji\""
  fi

  if [[ -z "$icon" ]]; then
    die "--icon required" $EXIT_USAGE "Usage: bcq messagetypes create \"Name\" --icon \"emoji\""
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local payload
  payload=$(jq -n --arg name "$name" --arg icon "$icon" '{name: $name, icon: $icon}')

  local response
  response=$(api_post "/buckets/$project/categories.json" "$payload")

  local type_id
  type_id=$(echo "$response" | jq -r '.id')
  local summary="Created message type #$type_id: $icon $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq messagetypes show $type_id -p $project" "View message type")" \
    "$(breadcrumb "list" "bcq messagetypes -p $project" "List message types")"
  )

  output "$response" "$summary" "$bcs"
}

# PUT /buckets/:bucket/categories/:id.json
_messagetype_update() {
  local type_id="" name="" icon="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --icon)
        [[ -z "${2:-}" ]] && die "--icon requires a value" $EXIT_USAGE
        icon="$2"
        shift 2
        ;;
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$type_id" ]]; then
          type_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$type_id" ]]; then
    die "Message type ID required" $EXIT_USAGE "Usage: bcq messagetypes update <id> [options]"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local payload="{}"

  if [[ -n "$name" ]]; then
    payload=$(echo "$payload" | jq --arg v "$name" '. + {name: $v}')
  fi

  if [[ -n "$icon" ]]; then
    payload=$(echo "$payload" | jq --arg v "$icon" '. + {icon: $v}')
  fi

  if [[ "$payload" == "{}" ]]; then
    die "No update fields provided" $EXIT_USAGE "Use --name or --icon"
  fi

  local response
  response=$(api_put "/buckets/$project/categories/$type_id.json" "$payload")

  local summary="Updated message type #$type_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq messagetypes show $type_id -p $project" "View message type")"
  )

  output "$response" "$summary" "$bcs"
}

# DELETE /buckets/:bucket/categories/:id.json
_messagetype_delete() {
  local type_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$type_id" ]]; then
          type_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$type_id" ]]; then
    die "Message type ID required" $EXIT_USAGE "Usage: bcq messagetypes delete <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  api_delete "/buckets/$project/categories/$type_id.json"

  local summary="Deleted message type #$type_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq messagetypes -p $project" "List message types")"
  )

  json_success "$summary" "$bcs"
}

_help_messagetypes() {
  cat <<'EOF'
## bcq messagetypes

Manage message types (categories) for the message board.

### Usage

    bcq messagetypes [options]                  List message types
    bcq messagetypes show <id> [options]        Show message type details
    bcq messagetypes create <name> [options]    Create new message type
    bcq messagetypes update <id> [options]      Update message type
    bcq messagetypes delete <id> [options]      Delete message type

### Options

    --project, -p <id>        Project ID (or set via config)
    --name <name>             Message type name
    --icon <emoji>            Message type icon (emoji)

### Examples

    # List message types in a project
    bcq messagetypes -p 123

    # Show a message type
    bcq messagetypes show 456 -p 123

    # Create a message type
    bcq messagetypes create "Announcement" --icon "📢" -p 123

    # Update a message type
    bcq messagetypes update 456 --name "Quick Update" --icon "⚡" -p 123

    # Delete a message type
    bcq messagetypes delete 456 -p 123

### Notes

Message types categorize messages on the message board. Each type has a name
and an emoji icon that appears alongside messages of that type.
EOF
}
