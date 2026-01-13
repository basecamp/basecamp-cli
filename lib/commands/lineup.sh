#!/usr/bin/env bash
# lineup.sh - Lineup markers management
# Covers:
#   - lineup_markers.md (3 endpoints: create, update, delete)
#
# Note: Lineup markers are account-wide, not project-scoped

cmd_lineup() {
  local action="${1:-}"

  case "$action" in
    create|add) shift; _lineup_marker_create "$@" ;;
    update) shift; _lineup_marker_update "$@" ;;
    delete|remove|rm) shift; _lineup_marker_delete "$@" ;;
    --help|-h) _help_lineup ;;
    "")
      die "Action required" $EXIT_USAGE "Run: bcq lineup --help"
      ;;
    *)
      die "Unknown lineup action: $action" $EXIT_USAGE "Run: bcq lineup --help"
      ;;
  esac
}

# POST /lineup/markers.json
_lineup_marker_create() {
  local name="" date=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name|-n)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --date|-d)
        [[ -z "${2:-}" ]] && die "--date requires a value" $EXIT_USAGE
        date="$2"
        shift 2
        ;;
      *)
        # First positional is name, second is date
        if [[ -z "$name" ]] && [[ ! "$1" =~ ^- ]]; then
          name="$1"
        elif [[ -z "$date" ]] && [[ ! "$1" =~ ^- ]]; then
          date="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$name" ]]; then
    die "Marker name required" $EXIT_USAGE "Usage: bcq lineup create <name> <date>"
  fi

  if [[ -z "$date" ]]; then
    die "Marker date required" $EXIT_USAGE "Usage: bcq lineup create <name> <date>"
  fi

  # Parse natural date if needed
  local parsed_date
  parsed_date=$(parse_date "$date")
  if [[ -z "$parsed_date" ]]; then
    parsed_date="$date"  # Fallback to raw value
  fi

  local payload
  payload=$(jq -n \
    --arg name "$name" \
    --arg date "$parsed_date" \
    '{name: $name, date: $date}')

  local response
  response=$(api_post "/lineup/markers.json" "$payload")

  local marker_id
  marker_id=$(echo "$response" | jq -r '.id // "unknown"')
  local summary="Created lineup marker #$marker_id: $name on $parsed_date"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq lineup update $marker_id --name \"...\" --date \"...\"" "Update marker")" \
    "$(breadcrumb "delete" "bcq lineup delete $marker_id" "Delete marker")"
  )

  output "$response" "$summary" "$bcs" "_lineup_marker_show_md"
}

# PUT /lineup/markers/:id.json
_lineup_marker_update() {
  local marker_id="" name="" date=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name|-n)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --date|-d)
        [[ -z "${2:-}" ]] && die "--date requires a value" $EXIT_USAGE
        date="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$marker_id" ]]; then
          marker_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$marker_id" ]]; then
    die "Marker ID required" $EXIT_USAGE "Usage: bcq lineup update <id> [--name <name>] [--date <date>]"
  fi

  if [[ -z "$name" ]] && [[ -z "$date" ]]; then
    die "Nothing to update" $EXIT_USAGE "Provide --name and/or --date"
  fi

  # Build payload with only provided fields
  local payload="{}"

  if [[ -n "$name" ]]; then
    payload=$(echo "$payload" | jq --arg name "$name" '. + {name: $name}')
  fi

  if [[ -n "$date" ]]; then
    local parsed_date
    parsed_date=$(parse_date "$date")
    if [[ -z "$parsed_date" ]]; then
      parsed_date="$date"
    fi
    payload=$(echo "$payload" | jq --arg date "$parsed_date" '. + {date: $date}')
  fi

  local response
  response=$(api_put "/lineup/markers/$marker_id.json" "$payload")

  local updated_name
  updated_name=$(echo "$response" | jq -r '.name // "Marker"')
  local summary="Updated lineup marker #$marker_id: $updated_name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "delete" "bcq lineup delete $marker_id" "Delete marker")"
  )

  output "$response" "$summary" "$bcs" "_lineup_marker_show_md"
}

# DELETE /lineup/markers/:id.json
_lineup_marker_delete() {
  local marker_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$marker_id" ]]; then
          marker_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$marker_id" ]]; then
    die "Marker ID required" $EXIT_USAGE "Usage: bcq lineup delete <id>"
  fi

  api_delete "/lineup/markers/$marker_id.json"

  local summary="Deleted lineup marker #$marker_id"

  # No response body for DELETE, create minimal response
  local response
  response=$(jq -n --arg id "$marker_id" '{id: ($id | tonumber), deleted: true}')

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq lineup create <name> <date>" "Create new marker")"
  )

  output "$response" "$summary" "$bcs" "_lineup_marker_deleted_md"
}

_lineup_marker_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local name date
  name=$(echo "$data" | jq -r '.name // "Marker"')
  date=$(echo "$data" | jq -r '.date // "-"')

  echo "## Lineup Marker: $name"
  echo

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "| Name | $name |"
  echo "| Date | $date |"
  echo "$data" | jq -r 'if .created_at then "| Created | \(.created_at | split("T")[0]) |" else empty end'
  echo

  md_breadcrumbs "$breadcrumbs"
}

_lineup_marker_deleted_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Lineup Marker Deleted"
  echo
  echo "$summary"
  echo

  md_breadcrumbs "$breadcrumbs"
}

_help_lineup() {
  cat <<'EOF'
## bcq lineup

Manage Lineup markers (account-wide date markers).

### Usage

    bcq lineup create <name> <date>              Create a new marker
    bcq lineup update <id> [options]             Update a marker
    bcq lineup delete <id>                       Delete a marker

### Options

    --name, -n <name>     Marker name
    --date, -d <date>     Marker date (YYYY-MM-DD or natural language)

### Examples

    # Create a milestone marker
    bcq lineup create "Alpha Release" 2024-03-15

    # Create with natural date
    bcq lineup create "Sprint End" "next friday"

    # Update marker name
    bcq lineup update 123 --name "Beta Release"

    # Update marker date
    bcq lineup update 123 --date 2024-04-01

    # Delete a marker
    bcq lineup delete 123

### Notes

Lineup markers are account-wide date markers that appear in the Lineup
view across all projects. They're useful for marking milestones, deadlines,
or other important dates visible to the entire team.

Unlike most bcq commands, lineup markers are not scoped to a project.
They apply to the entire Basecamp account.

The --date flag accepts natural language dates:
- Relative: today, tomorrow, +3, in 5 days
- Weekdays: monday, next friday
- Explicit: 2024-03-15 (YYYY-MM-DD)
EOF
}
