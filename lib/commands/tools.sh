#!/usr/bin/env bash
# tools.sh - Dock tools management (Campfire, Schedule, Docs, etc.)
# Covers tools API endpoints for managing project dock tools

cmd_tools() {
  local action="${1:-}"

  case "$action" in
    show|get) shift; _tools_show "$@" ;;
    create) shift; _tools_create "$@" ;;
    update|rename) shift; _tools_update "$@" ;;
    trash|delete) shift; _tools_trash "$@" ;;
    enable) shift; _tools_enable "$@" ;;
    disable) shift; _tools_disable "$@" ;;
    reposition|move) shift; _tools_reposition "$@" ;;
    --help|-h) _help_tools ;;
    "")
      _help_tools
      ;;
    *)
      # If numeric, show the tool
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _tools_show "$@"
      else
        die "Unknown tools action: $action" $EXIT_USAGE "Run: bcq tools --help"
      fi
      ;;
  esac
}

# GET /buckets/:bucket/dock/tools/:id.json
_tools_show() {
  local project="" tool_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    die "Tool ID required" $EXIT_USAGE "Usage: bcq tools show <id> --project <project>"
  fi

  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/dock/tools/$tool_id.json")

  local title tool_type position
  title=$(echo "$response" | jq -r '.title // "Tool"')
  tool_type=$(echo "$response" | jq -r '.type // "Unknown"')
  position=$(echo "$response" | jq -r '.position // "-"')
  local summary="$title ($tool_type) at position $position"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "rename" "bcq tools update $tool_id --title \"New Name\" --project $project" "Rename tool")" \
    "$(breadcrumb "reposition" "bcq tools reposition $tool_id --position 1 --project $project" "Move tool")" \
    "$(breadcrumb "project" "bcq projects $project" "View project")"
  )

  output "$response" "$summary" "$bcs" "_tools_show_md"
}

_tools_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title tool_type position status
  title=$(echo "$data" | jq -r '.title // "Tool"')
  tool_type=$(echo "$data" | jq -r '.type // "Unknown"')
  position=$(echo "$data" | jq -r '.position // "-"')
  status=$(echo "$data" | jq -r '.status // "active"')

  echo "## $title"
  echo
  echo "**Type**: $tool_type"
  echo "**Position**: $position"
  echo "**Status**: $status"
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/dock/tools.json
_tools_create() {
  local project="" source_id="" title=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --source|--clone|-s)
        [[ -z "${2:-}" ]] && die "--source requires a value" $EXIT_USAGE
        source_id="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_create
        return 0
        ;;
      *) shift ;;
    esac
  done

  if [[ -z "$source_id" ]]; then
    _help_tools_create
    die "Missing required --source flag (ID of tool to clone)" $EXIT_USAGE
  fi

  if [[ -z "$title" ]]; then
    _help_tools_create
    die "Missing required --title flag" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n \
    --arg source "$source_id" \
    --arg title "$title" \
    '{source_recording_id: ($source | tonumber), title: $title}')

  local response
  response=$(api_post "/buckets/$project/dock/tools.json" "$payload")

  local tool_title tool_id
  tool_title=$(echo "$response" | jq -r '.title // "Tool"')
  tool_id=$(echo "$response" | jq -r '.id')
  local summary="Created: $tool_title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "tool" "bcq tools $tool_id --project $project" "View tool")" \
    "$(breadcrumb "project" "bcq projects $project" "View project")"
  )

  output "$response" "$summary" "$bcs"
}

_help_tools_create() {
  cat << 'EOF'
bcq tools create - Create a new dock tool by cloning an existing one

USAGE
  bcq tools create --source <id> --title "name" [options]

OPTIONS
  --source, --clone, -s <id>  Source tool ID to clone (required)
  --title, -t <text>          Name for the new tool (required)
  --in, --project, -p <id>    Project ID or name

EXAMPLES
  # Clone a Campfire to create a second chat room
  bcq tools create --source 456 --title "Q&A Chat" --in 123

  # Clone a to-do list to create another
  bcq tools create --source 789 --title "Sprint Backlog" --in "My Project"
EOF
}

# PUT /buckets/:bucket/dock/tools/:id.json
_tools_update() {
  local project="" tool_id="" title=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_update
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    _help_tools_update
    die "Tool ID required" $EXIT_USAGE
  fi

  if [[ -z "$title" ]]; then
    _help_tools_update
    die "Missing required --title flag" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  local response
  response=$(api_put "/buckets/$project/dock/tools/$tool_id.json" "$payload")

  local tool_title
  tool_title=$(echo "$response" | jq -r '.title // "Tool"')
  local summary="Renamed to: $tool_title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "tool" "bcq tools $tool_id --project $project" "View tool")" \
    "$(breadcrumb "project" "bcq projects $project" "View project")"
  )

  output "$response" "$summary" "$bcs"
}

_help_tools_update() {
  cat << 'EOF'
bcq tools update - Rename a dock tool

USAGE
  bcq tools update <id> --title "new name" [options]

OPTIONS
  <id>                      Tool ID (required)
  --title, -t <text>        New name (required)
  --in, --project, -p <id>  Project ID or name

EXAMPLES
  bcq tools update 456 --title "Team Chat" --in 123
EOF
}

# DELETE /buckets/:bucket/dock/tools/:id.json
_tools_trash() {
  local project="" tool_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_trash
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    _help_tools_trash
    die "Tool ID required" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  api_delete "/buckets/$project/dock/tools/$tool_id.json"

  output '{"trashed": true}' "Tool $tool_id trashed"
}

_help_tools_trash() {
  cat << 'EOF'
bcq tools trash - Permanently trash a dock tool

USAGE
  bcq tools trash <id> [options]

OPTIONS
  <id>                      Tool ID (required)
  --in, --project, -p <id>  Project ID or name

WARNING
  This permanently removes the tool and all its content.

EXAMPLES
  bcq tools trash 456 --in 123
EOF
}

# POST /buckets/:bucket/recordings/:id/position.json
_tools_enable() {
  local project="" tool_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_enable
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    _help_tools_enable
    die "Tool ID required" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  api_post "/buckets/$project/recordings/$tool_id/position.json" "{}"

  output '{"enabled": true}' "Tool $tool_id enabled in dock"
}

_help_tools_enable() {
  cat << 'EOF'
bcq tools enable - Enable a tool in the project dock

USAGE
  bcq tools enable <id> [options]

OPTIONS
  <id>                      Tool ID (required)
  --in, --project, -p <id>  Project ID or name

EXAMPLES
  bcq tools enable 456 --in 123
EOF
}

# DELETE /buckets/:bucket/recordings/:id/position.json
_tools_disable() {
  local project="" tool_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_disable
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    _help_tools_disable
    die "Tool ID required" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  api_delete "/buckets/$project/recordings/$tool_id/position.json"

  output '{"disabled": true}' "Tool $tool_id disabled (hidden from dock)"
}

_help_tools_disable() {
  cat << 'EOF'
bcq tools disable - Disable a tool (hide from dock)

USAGE
  bcq tools disable <id> [options]

OPTIONS
  <id>                      Tool ID (required)
  --in, --project, -p <id>  Project ID or name

NOTE
  The tool is not deleted - just hidden. Use 'bcq tools enable' to restore.

EXAMPLES
  bcq tools disable 456 --in 123
EOF
}

# PUT /buckets/:bucket/recordings/:id/position.json
_tools_reposition() {
  local project="" tool_id="" position=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --position|--pos)
        [[ -z "${2:-}" ]] && die "--position requires a value" $EXIT_USAGE
        position="$2"
        shift 2
        ;;
      --help|-h)
        _help_tools_reposition
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$tool_id" ]]; then
          tool_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$tool_id" ]]; then
    _help_tools_reposition
    die "Tool ID required" $EXIT_USAGE
  fi

  if [[ -z "$position" ]]; then
    _help_tools_reposition
    die "Missing required --position flag" $EXIT_USAGE
  fi

  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --arg pos "$position" '{position: ($pos | tonumber)}')

  api_put "/buckets/$project/recordings/$tool_id/position.json" "$payload"

  output "{\"repositioned\": true, \"position\": $position}" "Tool $tool_id moved to position $position"
}

_help_tools_reposition() {
  cat << 'EOF'
bcq tools reposition - Change a tool's position in the dock

USAGE
  bcq tools reposition <id> --position <n> [options]

OPTIONS
  <id>                      Tool ID (required)
  --position, --pos <n>     New position, 1-based (required)
  --in, --project, -p <id>  Project ID or name

EXAMPLES
  bcq tools reposition 456 --position 1 --in 123   # Move to first
  bcq tools reposition 456 --position 3 --in 123   # Move to third
EOF
}

_help_tools() {
  cat <<'EOF'
## bcq tools

Manage project dock tools (Campfire, Schedule, Docs & Files, etc.).

### Usage

    bcq tools show <id> [options]                   Show a tool's details
    bcq tools create --source <id> --title [opts]   Create tool by cloning
    bcq tools update <id> --title "name" [opts]     Rename a tool
    bcq tools trash <id> [options]                  Permanently trash a tool
    bcq tools enable <id> [options]                 Enable tool in dock
    bcq tools disable <id> [options]                Disable tool (hide)
    bcq tools reposition <id> --position <n> [opts] Move tool in dock

### Options

    --in, -p <project>        Project ID or name

### Examples

    # View a tool
    bcq tools show 456 --in 123

    # Clone Campfire to create a second chat
    bcq tools create --source 456 --title "Q&A Chat" --in 123

    # Rename a tool
    bcq tools update 456 --title "Team Chat" --in 123

    # Move a tool to the first position
    bcq tools reposition 456 --position 1 --in 123

    # Hide a tool from the dock
    bcq tools disable 456 --in 123

    # Show it again
    bcq tools enable 456 --in 123

### Notes

Every project has a "dock" with tools like Message Board, To-dos, Docs & Files,
Campfire, Schedule, etc. Tool IDs can be found in the project's dock array
(see `bcq projects <id>`).

Tools can be created by cloning existing ones (e.g., create a second Campfire).
Disabling a tool hides it from the dock but preserves its content.

EOF
}
