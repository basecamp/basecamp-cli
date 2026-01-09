#!/usr/bin/env bash
# projects.sh - Project commands


cmd_projects() {
  local action="${1:-list}"
  shift || true

  case "$action" in
    list) _projects_list "$@" ;;
    get|show) _projects_show "$@" ;;
    --help|-h) _help_projects ;;
    *)
      # If it looks like an ID, show that project
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _projects_show "$action" "$@"
      else
        die "Unknown projects action: $action" $EXIT_USAGE "Run: bcq projects --help"
      fi
      ;;
  esac
}

_projects_list() {
  local status=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --status)
        status="$2"
        shift 2
        ;;
      --help|-h)
        _help_projects
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  local path="/projects.json"
  if [[ -n "$status" ]]; then
    path="/projects.json?status=$status"
  fi

  local response
  response=$(api_get "$path")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count projects"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq show project <id>" "Show project details")" \
    "$(breadcrumb "todos" "bcq todos --in <project>" "List todos in project")" \
    "$(breadcrumb "create_todo" "bcq todo \"content\" --in <project>" "Create todo in project")"
  )

  output "$response" "$summary" "$bcs" "_projects_list_md"
}

_projects_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Projects ($summary)"
  echo
  md_table "$data" "#:id" "Name:name" "Updated:updated_at"
  echo
  md_breadcrumbs "$breadcrumbs"
}

_projects_show() {
  local project_id="$1"

  if [[ -z "$project_id" ]]; then
    die "Project ID required" $EXIT_USAGE "Usage: bcq projects show <id>"
  fi

  local response
  response=$(api_get "/projects/$project_id.json")

  local name
  name=$(echo "$response" | jq -r '.name')
  local summary="Project: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "todos" "bcq todos --project $project_id" "List todos")" \
    "$(breadcrumb "todolists" "bcq todolists --project $project_id" "List todolists")" \
    "$(breadcrumb "people" "bcq people --project $project_id" "List members")"
  )

  output "$response" "$summary" "$bcs" "_projects_show_md"
}

_projects_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local name description created_at updated_at
  name=$(echo "$data" | jq -r '.name')
  description=$(echo "$data" | jq -r '.description // "No description"')
  created_at=$(echo "$data" | jq -r '.created_at')
  updated_at=$(echo "$data" | jq -r '.updated_at')

  echo "## $name"
  echo
  md_kv "Description" "$description" "Created" "$created_at" "Updated" "$updated_at"
  echo

  # Show dock items (tools enabled)
  local dock
  dock=$(echo "$data" | jq -r '.dock[]? | "- \(.title)"')
  if [[ -n "$dock" ]]; then
    echo "### Tools"
    echo "$dock"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}
