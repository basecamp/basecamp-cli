#!/usr/bin/env bash
# schedule.sh - Schedule and schedule entries management
# Covers schedules.md (2 endpoints) and schedule_entries.md (5 endpoints)

cmd_schedule() {
  local action="${1:-}"

  case "$action" in
    entries) shift; _schedule_entries "$@" ;;
    show) shift; _schedule_entry_show "$@" ;;
    create) shift; _schedule_entry_create "$@" ;;
    update) shift; _schedule_entry_update "$@" ;;
    settings) shift; _schedule_update "$@" ;;
    --help|-h) _help_schedule ;;
    "")
      _schedule_show "$@"
      ;;
    -*)
      # Flags go to schedule show
      _schedule_show "$@"
      ;;
    *)
      # If it looks like a numeric ID, show that entry
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _schedule_entry_show "$@"
      else
        die "Unknown schedule action: $action" $EXIT_USAGE "Run: bcq schedule --help"
      fi
      ;;
  esac
}

# Get schedule ID from project dock
_get_schedule_id() {
  local project="$1"
  local dock
  dock=$(api_get "/projects/$project.json" | jq -r '.dock[] | select(.name == "schedule") | .id')
  if [[ -z "$dock" ]] || [[ "$dock" == "null" ]]; then
    die "No schedule found for project $project" $EXIT_NOT_FOUND
  fi
  echo "$dock"
}

# GET /buckets/:bucket/schedules/:schedule.json
_schedule_show() {
  local project="" schedule_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --schedule|-s)
        [[ -z "${2:-}" ]] && die "--schedule requires a value" $EXIT_USAGE
        schedule_id="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project <id> or set default"
  fi

  if [[ -z "$schedule_id" ]]; then
    schedule_id=$(_get_schedule_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/schedules/$schedule_id.json")

  local entries_count include_due
  entries_count=$(echo "$response" | jq -r '.entries_count // 0')
  include_due=$(echo "$response" | jq -r '.include_due_assignments // false')
  local summary="$entries_count entries (include due assignments: $include_due)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "entries" "bcq schedule entries --project $project" "View schedule entries")" \
    "$(breadcrumb "create" "bcq schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project $project" "Create entry")"
  )

  output "$response" "$summary" "$bcs" "_schedule_show_md"
}

_schedule_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title bucket
  title=$(echo "$data" | jq -r '.title // "Schedule"')
  bucket=$(echo "$data" | jq -r '.bucket.name // "-"')

  echo "## $title in $bucket"
  echo
  echo "**Entries**: $(echo "$data" | jq -r '.entries_count // 0')"
  echo "**Include due assignments**: $(echo "$data" | jq -r '.include_due_assignments // false')"
  echo
  md_breadcrumbs "$breadcrumbs"
}

# PUT /buckets/:bucket/schedules/:schedule.json
_schedule_update() {
  local project="" schedule_id="" include_due=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --schedule|-s)
        [[ -z "${2:-}" ]] && die "--schedule requires a value" $EXIT_USAGE
        schedule_id="$2"
        shift 2
        ;;
      --include-due|--include-due-assignments)
        [[ -z "${2:-}" ]] && die "--include-due requires a value" $EXIT_USAGE
        include_due="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  # Validate required param first (before API calls)
  if [[ -z "$include_due" ]]; then
    die "--include-due required (true or false)" $EXIT_USAGE
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  if [[ -z "$schedule_id" ]]; then
    schedule_id=$(_get_schedule_id "$project")
  fi

  local payload
  payload=$(jq -n --arg include_due "$include_due" '{include_due_assignments: ($include_due == "true")}')

  local response
  response=$(api_put "/buckets/$project/schedules/$schedule_id.json" "$payload")

  local summary="✓ Updated schedule settings"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq schedule --project $project" "View schedule")"
  )

  output "$response" "$summary" "$bcs"
}

# GET /buckets/:bucket/schedules/:schedule/entries.json
_schedule_entries() {
  local project="" schedule_id="" status="active"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --schedule|-s)
        [[ -z "${2:-}" ]] && die "--schedule requires a value" $EXIT_USAGE
        schedule_id="$2"
        shift 2
        ;;
      --status)
        [[ -z "${2:-}" ]] && die "--status requires a value" $EXIT_USAGE
        status="$2"
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

  if [[ -z "$schedule_id" ]]; then
    schedule_id=$(_get_schedule_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/schedules/$schedule_id/entries.json?status=$status")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count schedule entries"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq schedule show <id> --project $project" "View entry details")" \
    "$(breadcrumb "create" "bcq schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project $project" "Create entry")"
  )

  output "$response" "$summary" "$bcs" "_schedule_entries_md"
}

_schedule_entries_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Schedule Entries ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No schedule entries found*"
  else
    echo "| # | Summary | Starts | Ends | All Day |"
    echo "|---|---------|--------|------|---------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.summary // .title | .[0:30]) | \(.starts_at | .[0:16]) | \(.ends_at | .[0:16]) | \(.all_day // false) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/schedule_entries/:id.json
# GET /buckets/:bucket/schedule_entries/:id/occurrences/:date.json (for recurring entries)
_schedule_entry_show() {
  local entry_id="" project="" occurrence_date=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --date|--occurrence)
        [[ -z "${2:-}" ]] && die "--date requires a value (YYYYMMDD)" $EXIT_USAGE
        occurrence_date="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$entry_id" ]]; then
          entry_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$entry_id" ]]; then
    die "Entry ID required" $EXIT_USAGE "Usage: bcq schedule show <entry_id> --project <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response path
  if [[ -n "$occurrence_date" ]]; then
    # Access specific occurrence of recurring entry
    path="/buckets/$project/schedule_entries/$entry_id/occurrences/$occurrence_date.json"
  else
    path="/buckets/$project/schedule_entries/$entry_id.json"
  fi
  response=$(api_get "$path")

  local summary starts_at ends_at
  summary=$(echo "$response" | jq -r '.summary // .title // "Entry"')
  starts_at=$(echo "$response" | jq -r '.starts_at // "-"')
  ends_at=$(echo "$response" | jq -r '.ends_at // "-"')

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq schedule update $entry_id --summary \"...\" --project $project" "Update entry")" \
    "$(breadcrumb "entries" "bcq schedule entries --project $project" "View all entries")"
  )

  output "$response" "$summary: $starts_at → $ends_at" "$bcs" "_schedule_entry_show_md"
}

_schedule_entry_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title all_day starts ends desc participants
  title=$(echo "$data" | jq -r '.summary // .title // "Schedule Entry"')
  all_day=$(echo "$data" | jq -r '.all_day // false')
  starts=$(echo "$data" | jq -r '.starts_at // "-"')
  ends=$(echo "$data" | jq -r '.ends_at // "-"')
  desc=$(echo "$data" | jq -r '.description // ""' | sed 's/<[^>]*>//g')

  echo "## $title"
  echo
  echo "**When**: $starts → $ends"
  [[ "$all_day" == "true" ]] && echo "**All Day**: Yes"

  participants=$(echo "$data" | jq -r '.participants // [] | length')
  if [[ "$participants" -gt 0 ]]; then
    echo "**Participants**: $(echo "$data" | jq -r '[.participants[].name] | join(", ")')"
  fi

  if [[ -n "$desc" ]] && [[ "$desc" != "null" ]]; then
    echo
    echo "$desc"
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/schedules/:schedule/entries.json
_schedule_entry_create() {
  local summary="" project="" schedule_id=""
  local starts_at="" ends_at="" description="" all_day="" notify=""
  local participant_ids=""

  # First positional arg is summary if not a flag
  if [[ $# -gt 0 ]] && [[ ! "$1" =~ ^- ]]; then
    summary="$1"
    shift
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --schedule|-s)
        [[ -z "${2:-}" ]] && die "--schedule requires a value" $EXIT_USAGE
        schedule_id="$2"
        shift 2
        ;;
      --summary|--title)
        [[ -z "${2:-}" ]] && die "--summary requires a value" $EXIT_USAGE
        summary="$2"
        shift 2
        ;;
      --starts-at|--start)
        [[ -z "${2:-}" ]] && die "--starts-at requires a value" $EXIT_USAGE
        starts_at="$2"
        shift 2
        ;;
      --ends-at|--end)
        [[ -z "${2:-}" ]] && die "--ends-at requires a value" $EXIT_USAGE
        ends_at="$2"
        shift 2
        ;;
      --description|--desc)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      --all-day)
        all_day="true"
        shift
        ;;
      --notify)
        notify="true"
        shift
        ;;
      --participants|--people)
        [[ -z "${2:-}" ]] && die "--participants requires a value" $EXIT_USAGE
        participant_ids="$2"
        shift 2
        ;;
      *)
        # Remaining positional is summary
        if [[ -z "$summary" ]] && [[ ! "$1" =~ ^- ]]; then
          summary="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$summary" ]]; then
    die "Summary required" $EXIT_USAGE "Usage: bcq schedule create \"Event title\" --starts-at <datetime> --ends-at <datetime>"
  fi

  if [[ -z "$starts_at" ]]; then
    die "--starts-at required (ISO 8601 datetime)" $EXIT_USAGE
  fi

  if [[ -z "$ends_at" ]]; then
    die "--ends-at required (ISO 8601 datetime)" $EXIT_USAGE
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  if [[ -z "$schedule_id" ]]; then
    schedule_id=$(_get_schedule_id "$project")
  fi

  # Build payload
  local payload
  payload=$(jq -n \
    --arg summary "$summary" \
    --arg starts_at "$starts_at" \
    --arg ends_at "$ends_at" \
    '{summary: $summary, starts_at: $starts_at, ends_at: $ends_at}')

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg desc "$description" '. + {description: $desc}')
  fi

  if [[ "$all_day" == "true" ]]; then
    payload=$(echo "$payload" | jq '. + {all_day: true}')
  fi

  if [[ "$notify" == "true" ]]; then
    payload=$(echo "$payload" | jq '. + {notify: true}')
  fi

  if [[ -n "$participant_ids" ]]; then
    local ids_array
    ids_array=$(echo "$participant_ids" | tr ',' '\n' | jq -R 'tonumber' | jq -s '.')
    payload=$(echo "$payload" | jq --argjson ids "$ids_array" '. + {participant_ids: $ids}')
  fi

  local response
  response=$(api_post "/buckets/$project/schedules/$schedule_id/entries.json" "$payload")

  local entry_id
  entry_id=$(echo "$response" | jq -r '.id')
  local result_summary="✓ Created schedule entry #$entry_id: $summary"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq schedule show $entry_id --project $project" "View entry")" \
    "$(breadcrumb "entries" "bcq schedule entries --project $project" "View all entries")"
  )

  output "$response" "$result_summary" "$bcs"
}

# PUT /buckets/:bucket/schedule_entries/:id.json
_schedule_entry_update() {
  local entry_id="" project=""
  local summary="" starts_at="" ends_at="" description="" all_day="" notify=""
  local participant_ids=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --summary|--title)
        [[ -z "${2:-}" ]] && die "--summary requires a value" $EXIT_USAGE
        summary="$2"
        shift 2
        ;;
      --starts-at|--start)
        [[ -z "${2:-}" ]] && die "--starts-at requires a value" $EXIT_USAGE
        starts_at="$2"
        shift 2
        ;;
      --ends-at|--end)
        [[ -z "${2:-}" ]] && die "--ends-at requires a value" $EXIT_USAGE
        ends_at="$2"
        shift 2
        ;;
      --description|--desc)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      --all-day)
        all_day="true"
        shift
        ;;
      --notify)
        notify="true"
        shift
        ;;
      --participants|--people)
        [[ -z "${2:-}" ]] && die "--participants requires a value" $EXIT_USAGE
        participant_ids="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$entry_id" ]]; then
          entry_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$entry_id" ]]; then
    die "Entry ID required" $EXIT_USAGE "Usage: bcq schedule update <entry_id> [options]"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Build payload with provided fields only
  local payload="{}"

  if [[ -n "$summary" ]]; then
    payload=$(echo "$payload" | jq --arg v "$summary" '. + {summary: $v}')
  fi

  if [[ -n "$starts_at" ]]; then
    payload=$(echo "$payload" | jq --arg v "$starts_at" '. + {starts_at: $v}')
  fi

  if [[ -n "$ends_at" ]]; then
    payload=$(echo "$payload" | jq --arg v "$ends_at" '. + {ends_at: $v}')
  fi

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg v "$description" '. + {description: $v}')
  fi

  if [[ "$all_day" == "true" ]]; then
    payload=$(echo "$payload" | jq '. + {all_day: true}')
  fi

  if [[ "$notify" == "true" ]]; then
    payload=$(echo "$payload" | jq '. + {notify: true}')
  fi

  if [[ -n "$participant_ids" ]]; then
    local ids_array
    ids_array=$(echo "$participant_ids" | tr ',' '\n' | jq -R 'tonumber' | jq -s '.')
    payload=$(echo "$payload" | jq --argjson ids "$ids_array" '. + {participant_ids: $ids}')
  fi

  if [[ "$payload" == "{}" ]]; then
    die "No update fields provided" $EXIT_USAGE "Use --summary, --starts-at, --ends-at, etc."
  fi

  local response
  response=$(api_put "/buckets/$project/schedule_entries/$entry_id.json" "$payload")

  local result_summary="✓ Updated schedule entry #$entry_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq schedule show $entry_id --project $project" "View entry")"
  )

  output "$response" "$result_summary" "$bcs"
}

_help_schedule() {
  cat <<'EOF'
## bcq schedule

Manage project schedules and schedule entries.

### Usage

    bcq schedule [options]                      Show project schedule
    bcq schedule entries [options]              List schedule entries
    bcq schedule show <entry_id> [options]      Show a schedule entry
    bcq schedule show <entry_id> --date <YYYYMMDD>  Show recurring entry occurrence
    bcq schedule create <summary> [options]     Create a schedule entry
    bcq schedule update <entry_id> [options]    Update a schedule entry
    bcq schedule settings [options]             Update schedule settings

### Options

    --in, -p <project>        Project ID
    --schedule, -s <id>       Schedule ID (auto-detected from dock)
    --status <status>         Filter entries: active, archived, trashed
    --date <YYYYMMDD>         Access specific occurrence of recurring entry

### Entry Options (for create/update)

    --summary <text>          Event title/summary (required for create)
    --starts-at <datetime>    Start time in ISO 8601 format (required for create)
    --ends-at <datetime>      End time in ISO 8601 format (required for create)
    --description <html>      Detailed description
    --all-day                 Mark as all-day event
    --notify                  Notify participants
    --participants <ids>      Comma-separated person IDs

### Schedule Settings Options

    --include-due <bool>      Include due dates from todos/cards in schedule

### Examples

    # View project schedule
    bcq schedule --project 123

    # List schedule entries
    bcq schedule entries --project 123

    # View a specific entry
    bcq schedule show 456 --project 123

    # View specific occurrence of recurring entry (by date)
    bcq schedule show 456 --date 20240115 --project 123

    # Create a schedule entry
    bcq schedule create "Team Meeting" \
      --starts-at "2024-01-15T10:00:00Z" \
      --ends-at "2024-01-15T11:00:00Z" \
      --project 123

    # Create an all-day event
    bcq schedule create "Company Holiday" \
      --starts-at "2024-01-01" \
      --ends-at "2024-01-01" \
      --all-day \
      --project 123

    # Update an entry
    bcq schedule update 456 --summary "Updated Meeting" --project 123

    # Update schedule settings
    bcq schedule settings --include-due false --project 123

EOF
}
