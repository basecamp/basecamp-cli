#!/usr/bin/env bash
# timesheet.sh - Timesheet reports
# Covers timesheets.md (3 endpoints)

cmd_timesheet() {
  local action="${1:-}"

  case "$action" in
    report) shift; _timesheet_report "$@" ;;
    project) shift; _timesheet_project "$@" ;;
    recording) shift; _timesheet_recording "$@" ;;
    --help|-h) _help_timesheet ;;
    "")
      # Default: show account-wide report
      _timesheet_report "$@"
      ;;
    -*)
      # Flags go to report
      _timesheet_report "$@"
      ;;
    *)
      # If numeric, could be project id
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _timesheet_project "$@"
      else
        die "Unknown timesheet action: $action" $EXIT_USAGE "Run: bcq timesheet --help"
      fi
      ;;
  esac
}

# GET /reports/timesheet.json
_timesheet_report() {
  local start_date="" end_date="" person_id="" bucket_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --start|--from)
        [[ -z "${2:-}" ]] && die "--start requires a value" $EXIT_USAGE
        start_date="$2"
        shift 2
        ;;
      --end|--to)
        [[ -z "${2:-}" ]] && die "--end requires a value" $EXIT_USAGE
        end_date="$2"
        shift 2
        ;;
      --person)
        [[ -z "${2:-}" ]] && die "--person requires a value" $EXIT_USAGE
        person_id="$2"
        shift 2
        ;;
      --bucket|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        bucket_id="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  # Validate: if one date is provided, both are required
  if [[ -n "$start_date" ]] && [[ -z "$end_date" ]]; then
    die "--end required when --start is provided" $EXIT_USAGE
  fi
  if [[ -n "$end_date" ]] && [[ -z "$start_date" ]]; then
    die "--start required when --end is provided" $EXIT_USAGE
  fi

  # Build query params
  local query_params=""
  [[ -n "$start_date" ]] && query_params="${query_params}&start_date=$start_date"
  [[ -n "$end_date" ]] && query_params="${query_params}&end_date=$end_date"
  [[ -n "$person_id" ]] && query_params="${query_params}&person_id=$person_id"
  [[ -n "$bucket_id" ]] && query_params="${query_params}&bucket_id=$bucket_id"

  # Remove leading &
  query_params="${query_params#&}"

  local path="/reports/timesheet.json"
  [[ -n "$query_params" ]] && path="${path}?${query_params}"

  local response
  response=$(api_get "$path")

  local count total_hours
  count=$(echo "$response" | jq 'length')
  total_hours=$(echo "$response" | jq '[.[].hours | tonumber] | add // 0')
  local summary="$count timesheet entries (${total_hours}h total)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "project" "bcq timesheet project <id>" "View project timesheet")" \
    "$(breadcrumb "recording" "bcq timesheet recording <id> --project <project>" "View recording timesheet")"
  )

  output "$response" "$summary" "$bcs" "_timesheet_report_md"
}

_timesheet_report_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Timesheet Report ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No timesheet entries*"
  else
    echo "| Date | Person | Project | Description | Hours |"
    echo "|------|--------|---------|-------------|-------|"
    echo "$data" | jq -r '.[] | "| \(.date) | \(.creator.name // "-") | \(.bucket.name // "-" | .[0:20]) | \(.description // "-" | .[0:30]) | \(.hours) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /projects/:id/timesheet.json
_timesheet_project() {
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$project" ]]; then
          project="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "Project ID required" $EXIT_USAGE "Usage: bcq timesheet project <id>"
  fi

  local response
  response=$(api_get "/projects/$project/timesheet.json")

  local count total_hours
  count=$(echo "$response" | jq 'length')
  total_hours=$(echo "$response" | jq '[.[].hours | tonumber] | add // 0')
  local summary="$count entries (${total_hours}h total)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "report" "bcq timesheet report" "View account-wide report")" \
    "$(breadcrumb "recording" "bcq timesheet recording <id> --project $project" "View recording timesheet")"
  )

  output "$response" "$summary" "$bcs" "_timesheet_project_md"
}

_timesheet_project_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Project Timesheet ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No timesheet entries*"
  else
    echo "| Date | Person | Parent | Description | Hours |"
    echo "|------|--------|--------|-------------|-------|"
    echo "$data" | jq -r '.[] | "| \(.date) | \(.creator.name // "-") | \(.parent.title // "-" | .[0:25]) | \(.description // "-" | .[0:25]) | \(.hours) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /projects/:id/recordings/:id/timesheet.json
_timesheet_recording() {
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq timesheet recording <id> --project <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project <id> or set default"
  fi

  local response
  response=$(api_get "/projects/$project/recordings/$recording_id/timesheet.json")

  local count total_hours
  count=$(echo "$response" | jq 'length')
  total_hours=$(echo "$response" | jq '[.[].hours | tonumber] | add // 0')
  local summary="$count entries (${total_hours}h total)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "project" "bcq timesheet project $project" "View project timesheet")" \
    "$(breadcrumb "report" "bcq timesheet report" "View account-wide report")"
  )

  output "$response" "$summary" "$bcs" "_timesheet_recording_md"
}

_timesheet_recording_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Recording Timesheet ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No timesheet entries*"
  else
    echo "| Date | Person | Description | Hours |"
    echo "|------|--------|-------------|-------|"
    echo "$data" | jq -r '.[] | "| \(.date) | \(.creator.name // "-") | \(.description // "-" | .[0:40]) | \(.hours) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_help_timesheet() {
  cat <<'EOF'
## bcq timesheet

View timesheet reports.

### Usage

    bcq timesheet [options]                             Account-wide report (last month)
    bcq timesheet report [options]                      Account-wide report with filters
    bcq timesheet project <id>                          Project timesheet
    bcq timesheet recording <id> --project <project>    Recording timesheet

### Options (for report)

    --start, --from <date>    Start date (ISO 8601, e.g., 2024-01-01)
    --end, --to <date>        End date (ISO 8601)
    --person <id>             Filter by person ID
    --project, -p <id>        Filter by project ID

### Examples

    # View last month's timesheet entries
    bcq timesheet

    # View timesheet for specific date range
    bcq timesheet report --start 2024-01-01 --end 2024-01-31

    # View timesheet filtered by person
    bcq timesheet report --person 12345

    # View project timesheet
    bcq timesheet project 123

    # View timesheet for a specific recording
    bcq timesheet recording 456 --project 123

### Notes

Timesheet entries track time logged against any recording (todo, message,
document, etc.). The account-wide report defaults to the last month if no
date range is specified.

EOF
}
