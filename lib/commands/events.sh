#!/usr/bin/env bash
# events.sh - View recording event history (audit trail)
# Covers events.md (1 endpoint)
# Events track all changes to a recording: created, completed, assignment_changed, etc.

cmd_events() {
  local action="${1:-}"

  case "$action" in
    --help|-h) _help_events ;;
    "")
      die "Recording ID required" $EXIT_USAGE "Usage: bcq events <recording_id> --project <id>"
      ;;
    -*)
      # Flags without recording ID
      die "Recording ID required" $EXIT_USAGE "Usage: bcq events <recording_id> --project <id>"
      ;;
    *)
      # If numeric, treat as recording ID
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _events_list "$@"
      else
        die "Recording ID required" $EXIT_USAGE "Usage: bcq events <recording_id> --project <id>"
      fi
      ;;
  esac
}

# GET /buckets/:bucket/recordings/:id/events.json
_events_list() {
  local recording_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq events <recording_id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/recordings/$recording_id/events.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count events for recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "recording" "bcq show $recording_id -p $project" "View the recording")"
  )

  output "$response" "$summary" "$bcs" "_events_list_md"
}

_events_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Events ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No events found*"
  else
    echo "| Time | Action | By |"
    echo "|------|--------|----|"
    echo "$data" | jq -r '.[] | "\(.created_at | split("T") | .[0] + " " + (.[1] | split(".")[0]))\t\(.action)\t\(.creator.name // "Unknown")"' | \
      while IFS=$'\t' read -r time action by; do
        echo "| $time | $action | $by |"
      done
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_help_events() {
  cat <<'EOF'
## bcq events

View the event history (audit trail) for any recording.

### Usage

    bcq events <recording_id> --project <id>

### Options

    --project, -p <id>        Project ID (or set via config)

### Examples

    # View events for a todo
    bcq events 12345 -p 123

    # View events for a message
    bcq events 67890 -p 123

### Notes

Events track all changes to a recording. Common event actions:
- created - Recording was created
- completed/uncompleted - Todo completion state changed
- assignment_changed - Assignees were added/removed
- content_changed - Content was edited
- archived/unarchived - Recording status changed
- commented_on - A comment was added

Events show who made each change and when.
EOF
}
