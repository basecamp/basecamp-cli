#!/usr/bin/env bash
# todosets.sh - Todoset container resource
# Covers todosets.md (1 endpoint)

cmd_todosets() {
  local action="${1:-}"

  case "$action" in
    show|get) shift; _todoset_show "$@" ;;
    --help|-h) _help_todosets ;;
    "")
      _todoset_show "$@"
      ;;
    -*)
      # Flags go to todoset show
      _todoset_show "$@"
      ;;
    *)
      # If numeric, treat as todoset ID
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _todoset_show "$@"
      else
        die "Unknown todosets action: $action" $EXIT_USAGE "Run: bcq todosets --help"
      fi
      ;;
  esac
}

# Get todoset ID from project dock
_get_todoset_id() {
  local project="$1"
  local dock
  dock=$(api_get "/projects/$project.json" | jq -r '.dock[] | select(.name == "todoset") | .id')
  if [[ -z "$dock" ]] || [[ "$dock" == "null" ]]; then
    die "No todoset found for project $project" $EXIT_NOT_FOUND
  fi
  echo "$dock"
}

# GET /buckets/:bucket/todosets/:todoset.json
_todoset_show() {
  local project="" todoset_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --todoset|-t)
        [[ -z "${2:-}" ]] && die "--todoset requires a value" $EXIT_USAGE
        todoset_id="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$todoset_id" ]]; then
          todoset_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project <id> or set default"
  fi

  if [[ -z "$todoset_id" ]]; then
    todoset_id=$(_get_todoset_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/todosets/$todoset_id.json")

  local todolists_count completed_ratio
  todolists_count=$(echo "$response" | jq -r '.todolists_count // 0')
  completed_ratio=$(echo "$response" | jq -r '.completed_ratio // "0.0"')
  local summary="$todolists_count todolists (${completed_ratio}% complete)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "todolists" "bcq todolists --project $project" "List all todolists")" \
    "$(breadcrumb "project" "bcq projects show $project" "View project details")"
  )

  output "$response" "$summary" "$bcs" "_todoset_show_md"
}

_todoset_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title todolists_url
  title=$(echo "$data" | jq -r '.title // "Todoset"')
  todolists_url=$(echo "$data" | jq -r '.todolists_url // ""')

  echo "## $title"
  echo
  echo "**$summary**"
  echo
  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "$data" | jq -r '"| Todolists | \(.todolists_count // 0) |"'
  echo "$data" | jq -r '"| Completed | \(.completed_ratio // "0.0")% |"'
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo
  md_breadcrumbs "$breadcrumbs"
}

_help_todosets() {
  cat <<'EOF'
## bcq todosets

View todoset container for a project.

### Usage

    bcq todosets [--project <id>]                   Show project's todoset
    bcq todosets show [<id>] [--project <id>]      Show specific todoset

### Options

    --project, -p <id>    Project ID (or uses default)
    --todoset, -t <id>    Todoset ID (auto-detected from project if omitted)

### Examples

    # View todoset for current project
    bcq todosets

    # View todoset for specific project
    bcq todosets --project 123

    # View specific todoset by ID
    bcq todosets 456 --project 123

### Notes

A todoset is the container that holds all todolists in a project. Each project
has exactly one todoset in its dock.

EOF
}
