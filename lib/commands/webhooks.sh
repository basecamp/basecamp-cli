#!/usr/bin/env bash
# webhooks.sh - Webhook management commands

cmd_webhooks() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _webhooks_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _webhooks_list "$@" ;;
    get|show) _webhooks_show "$@" ;;
    create) _webhooks_create "$@" ;;
    update) _webhooks_update "$@" ;;
    delete|destroy) _webhooks_delete "$@" ;;
    --help|-h) _help_webhooks ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _webhooks_show "$action" "$@"
      else
        die "Unknown webhooks action: $action" $EXIT_USAGE "Run: bcq webhooks --help"
      fi
      ;;
  esac
}

_webhooks_list() {
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_webhooks
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified. Use --in <project>" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/webhooks.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count webhooks"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq webhooks <id> --in $project" "Show webhook details")" \
    "$(breadcrumb "create" "bcq webhooks create --url <url> --in $project" "Create webhook")"
  )

  output "$response" "$summary" "$bcs" "_webhooks_list_md"
}

_webhooks_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Webhooks ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No webhooks configured*"
  else
    echo "| ID | URL | Types | Active |"
    echo "|----|-----|-------|--------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.payload_url | .[0:40]) | \(.types | join(", ") | .[0:20]) | \(.active) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_webhooks_show() {
  local webhook_id="${1:-}"
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

  if [[ -z "$webhook_id" ]]; then
    die "Webhook ID required" $EXIT_USAGE "Usage: bcq webhooks show <id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local response
  response=$(api_get "/buckets/$project/webhooks/$webhook_id.json")

  local url
  url=$(echo "$response" | jq -r '.payload_url // "Unknown"')
  local summary="Webhook #$webhook_id: $url"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq webhooks update $webhook_id --in $project" "Update webhook")" \
    "$(breadcrumb "delete" "bcq webhooks delete $webhook_id --in $project" "Delete webhook")" \
    "$(breadcrumb "list" "bcq webhooks --in $project" "Back to webhooks")"
  )

  output "$response" "$summary" "$bcs" "_webhooks_show_md"
}

_webhooks_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id url types active
  id=$(echo "$data" | jq -r '.id')
  url=$(echo "$data" | jq -r '.payload_url')
  types=$(echo "$data" | jq -r '.types | join(", ")')
  active=$(echo "$data" | jq -r '.active')

  echo "## Webhook #$id"
  echo
  md_kv "URL" "$url" \
        "Types" "$types" \
        "Active" "$active"

  # Show recent deliveries if present
  local deliveries_count
  deliveries_count=$(echo "$data" | jq '.recent_deliveries | length // 0')
  if [[ "$deliveries_count" -gt 0 ]]; then
    echo "### Recent Deliveries"
    echo
    echo "| Time | Event | Status |"
    echo "|------|-------|--------|"
    echo "$data" | jq -r '.recent_deliveries[:5][] | "| \(.created_at | split("T")[0]) | \(.request.body.kind // "-") | \(.response.code // "-") |"'
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

_webhooks_create() {
  local project="" url="" types=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --url)
        [[ -z "${2:-}" ]] && die "--url requires a value" $EXIT_USAGE
        url="$2"
        shift 2
        ;;
      --types)
        [[ -z "${2:-}" ]] && die "--types requires a value" $EXIT_USAGE
        types="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  if [[ -z "$url" ]]; then
    die "Webhook URL required" $EXIT_USAGE "Usage: bcq webhooks create --url <https://...> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local payload
  if [[ -n "$types" ]]; then
    # Convert comma-separated to JSON array
    local types_json
    types_json=$(echo "$types" | jq -R 'split(",") | map(gsub("^\\s+|\\s+$"; ""))')
    payload=$(jq -n --arg url "$url" --argjson types "$types_json" '{payload_url: $url, types: $types}')
  else
    payload=$(jq -n --arg url "$url" '{payload_url: $url}')
  fi

  local response
  response=$(api_post "/buckets/$project/webhooks.json" "$payload")

  local webhook_id
  webhook_id=$(echo "$response" | jq -r '.id')
  local summary="Created webhook #$webhook_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq webhooks $webhook_id --in $project" "View webhook")" \
    "$(breadcrumb "list" "bcq webhooks --in $project" "List webhooks")"
  )

  output "$response" "$summary" "$bcs"
}

_webhooks_update() {
  local webhook_id="" project="" url="" types="" active=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --url)
        [[ -z "${2:-}" ]] && die "--url requires a value" $EXIT_USAGE
        url="$2"
        shift 2
        ;;
      --types)
        [[ -z "${2:-}" ]] && die "--types requires a value" $EXIT_USAGE
        types="$2"
        shift 2
        ;;
      --active)
        active="true"
        shift
        ;;
      --inactive)
        active="false"
        shift
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$webhook_id" ]]; then
          webhook_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$webhook_id" ]]; then
    die "Webhook ID required" $EXIT_USAGE "Usage: bcq webhooks update <id> --url <url> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  # Build payload with only specified fields
  local payload="{}"
  [[ -n "$url" ]] && payload=$(echo "$payload" | jq --arg url "$url" '. + {payload_url: $url}')
  [[ -n "$active" ]] && payload=$(echo "$payload" | jq --argjson active "$active" '. + {active: $active}')
  if [[ -n "$types" ]]; then
    local types_json
    types_json=$(echo "$types" | jq -R 'split(",") | map(gsub("^\\s+|\\s+$"; ""))')
    payload=$(echo "$payload" | jq --argjson types "$types_json" '. + {types: $types}')
  fi

  local response
  response=$(api_put "/buckets/$project/webhooks/$webhook_id.json" "$payload")

  local summary="Updated webhook #$webhook_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq webhooks $webhook_id --in $project" "View webhook")" \
    "$(breadcrumb "list" "bcq webhooks --in $project" "List webhooks")"
  )

  output "$response" "$summary" "$bcs"
}

_webhooks_delete() {
  local webhook_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$webhook_id" ]]; then
          webhook_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$webhook_id" ]]; then
    die "Webhook ID required" $EXIT_USAGE "Usage: bcq webhooks delete <id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  api_delete "/buckets/$project/webhooks/$webhook_id.json" >/dev/null

  local summary="Deleted webhook #$webhook_id"
  local response='{}'

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq webhooks --in $project" "List webhooks")"
  )

  output "$response" "$summary" "$bcs"
}

_help_webhooks() {
  cat <<'EOF'
## bcq webhooks

Manage webhooks for project notifications.

### Usage

    bcq webhooks [action] [options]

### Actions

    list              List webhooks (default)
    show <id>         Show webhook details and recent deliveries
    create            Create a new webhook
    update <id>       Update webhook settings
    delete <id>       Delete a webhook

### Options

    --in, -p <project>    Project ID
    --url <https://...>   Webhook payload URL (must be HTTPS)
    --types <types>       Comma-separated event types (default: all)
    --active              Enable webhook
    --inactive            Disable webhook

### Event Types

    Todo, Todolist, Message, Comment, Document, Upload,
    Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer

### Examples

    # List webhooks
    bcq webhooks --in 12345

    # Create webhook for all events
    bcq webhooks create --url https://example.com/hook --in 12345

    # Create webhook for specific types
    bcq webhooks create --url https://example.com/hook --types "Todo,Todolist" --in 12345

    # View webhook with recent deliveries
    bcq webhooks show 67890 --in 12345

    # Disable webhook
    bcq webhooks update 67890 --inactive --in 12345

    # Delete webhook
    bcq webhooks delete 67890 --in 12345

EOF
}
