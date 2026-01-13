#!/usr/bin/env bash
# subscriptions.sh - Manage recording subscriptions (notifications)

cmd_subscriptions() {
  local action="${1:-}"

  case "$action" in
    show|get|"") shift || true; _subscriptions_show "$@" ;;
    subscribe) shift; _subscriptions_subscribe "$@" ;;
    unsubscribe) shift; _subscriptions_unsubscribe "$@" ;;
    add) shift; _subscriptions_update "add" "$@" ;;
    remove) shift; _subscriptions_update "remove" "$@" ;;
    --help|-h) _help_subscriptions ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _subscriptions_show "$@"
      else
        die "Unknown subscriptions action: $action" $EXIT_USAGE "Run: bcq subscriptions --help"
      fi
      ;;
  esac
}

_subscriptions_show() {
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq subscriptions <recording_id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/recordings/$recording_id/subscription.json")

  local count subscribed
  count=$(echo "$response" | jq -r '.count // 0')
  subscribed=$(echo "$response" | jq -r '.subscribed')
  local summary="$count subscribers (you: $subscribed)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "subscribe" "bcq subscriptions subscribe $recording_id --in $project" "Subscribe yourself")" \
    "$(breadcrumb "unsubscribe" "bcq subscriptions unsubscribe $recording_id --in $project" "Unsubscribe yourself")"
  )

  output "$response" "$summary" "$bcs" "_subscriptions_show_md"
}

_subscriptions_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Subscriptions ($summary)"
  echo

  local count
  count=$(echo "$data" | jq '.subscribers | length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No subscribers*"
  else
    echo "| Name | Email |"
    echo "|------|-------|"
    echo "$data" | jq -r '.subscribers[] | "| \(.name) | \(.email_address) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_subscriptions_subscribe() {
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq subscriptions subscribe <recording_id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_post "/buckets/$project/recordings/$recording_id/subscription.json" "{}")

  local summary="✓ Subscribed to recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq subscriptions $recording_id --in $project" "View subscribers")" \
    "$(breadcrumb "unsubscribe" "bcq subscriptions unsubscribe $recording_id --in $project" "Unsubscribe")"
  )

  output "$response" "$summary" "$bcs"
}

_subscriptions_unsubscribe() {
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq subscriptions unsubscribe <recording_id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  api_delete "/buckets/$project/recordings/$recording_id/subscription.json" >/dev/null 2>&1 || true

  local summary="✓ Unsubscribed from recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq subscriptions $recording_id --in $project" "View subscribers")" \
    "$(breadcrumb "subscribe" "bcq subscriptions subscribe $recording_id --in $project" "Re-subscribe")"
  )

  output '{}' "$summary" "$bcs"
}

_subscriptions_update() {
  local mode="$1"
  shift
  local recording_id="" project="" people_ids=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --people|--ids)
        [[ -z "${2:-}" ]] && die "--people requires a value" $EXIT_USAGE
        people_ids="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$recording_id" ]]; then
          recording_id="$1"
        elif [[ "$1" =~ ^[0-9,]+$ ]] && [[ -z "$people_ids" ]]; then
          people_ids="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$recording_id" ]]; then
    die "Recording ID required" $EXIT_USAGE "Usage: bcq subscriptions $mode <recording_id> <person_ids> --in <project>"
  fi

  if [[ -z "$people_ids" ]]; then
    die "Person ID(s) required" $EXIT_USAGE "Provide comma-separated person IDs"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Convert comma-separated IDs to JSON array
  local ids_array
  ids_array=$(echo "$people_ids" | tr ',' '\n' | jq -R 'tonumber' | jq -s '.')

  local payload
  if [[ "$mode" == "add" ]]; then
    payload=$(jq -n --argjson ids "$ids_array" '{subscriptions: $ids}')
  else
    payload=$(jq -n --argjson ids "$ids_array" '{unsubscriptions: $ids}')
  fi

  local response
  response=$(api_put "/buckets/$project/recordings/$recording_id/subscription.json" "$payload")

  local action_word
  [[ "$mode" == "add" ]] && action_word="Added" || action_word="Removed"
  local summary="✓ $action_word subscribers for recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq subscriptions $recording_id --in $project" "View subscribers")"
  )

  output "$response" "$summary" "$bcs"
}

_help_subscriptions() {
  cat <<'EOF'
## bcq subscriptions

Manage recording subscriptions (who gets notified on changes).

### Usage

    bcq subscriptions <recording_id> [options]
    bcq subscriptions <action> <recording_id> [options]

### Actions

    show              Show current subscribers (default)
    subscribe         Subscribe yourself to recording
    unsubscribe       Unsubscribe yourself from recording
    add               Add people to subscribers
    remove            Remove people from subscribers

### Options

    --in, -p <project>      Project ID
    --people <ids>          Comma-separated person IDs (for add/remove)

### Examples

    # View subscribers
    bcq subscriptions 12345 --in 67890

    # Subscribe yourself
    bcq subscriptions subscribe 12345 --in 67890

    # Unsubscribe yourself
    bcq subscriptions unsubscribe 12345 --in 67890

    # Add people to subscribers
    bcq subscriptions add 12345 111,222,333 --in 67890

    # Remove people from subscribers
    bcq subscriptions remove 12345 111,222 --in 67890

EOF
}
