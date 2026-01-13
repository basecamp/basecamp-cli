#!/usr/bin/env bash
# todolists.sh - Todolist commands


cmd_todolists() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _todolists_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _todolists_list "$@" ;;
    show|get) _todolists_show "$@" ;;
    --help|-h) _help_todolists ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _todolists_show "$action" "$@"
      else
        die "Unknown todolists action: $action" $EXIT_USAGE "Run: bcq todolists --help"
      fi
      ;;
  esac
}


_todolists_list() {
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_todolists
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

  # Get todoset from project dock
  local project_data todoset_id
  project_data=$(api_get "/projects/$project.json")
  todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')

  if [[ -z "$todoset_id" ]]; then
    die "No todoset found in project $project" $EXIT_NOT_FOUND
  fi

  # Get todolists
  local response
  response=$(api_get "/buckets/$project/todosets/$todoset_id/todolists.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count todolists"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "todos" "bcq todos --list <id> --in $project" "List todos in list")" \
    "$(breadcrumb "create" "bcq todo \"content\" --list <id> --in $project" "Create todo")"
  )

  output "$response" "$summary" "$bcs" "_todolists_list_md"
}


_todolists_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Todolists ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No todolists found*"
  else
    echo "| # | Name | Todos | Completed |"
    echo "|---|------|-------|-----------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.name) | \(.todos_remaining_count // 0) | \(.completed_ratio // "0%") |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}


_todolists_show() {
  local todolist_id="" project=""

  # Parse all arguments in single pass
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|--in|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq todolists --help"
        ;;
      *)
        # Positional: todolist ID
        if [[ -z "$todolist_id" ]]; then
          todolist_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$todolist_id" ]]; then
    die "Todolist ID required" $EXIT_USAGE "Usage: bcq todolists show <id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  local response
  response=$(api_get "/buckets/$project/todolists/$todolist_id.json")

  local name
  name=$(echo "$response" | jq -r '.name')
  local summary="Todolist: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "todos" "bcq todos --list $todolist_id --in $project" "List todos")" \
    "$(breadcrumb "create" "bcq todo \"content\" --list $todolist_id --in $project" "Create todo")"
  )

  output "$response" "$summary" "$bcs" "_todolists_show_md"
}


_todolists_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id name description remaining completed_ratio
  id=$(echo "$data" | jq -r '.id')
  name=$(echo "$data" | jq -r '.name')
  description=$(echo "$data" | jq -r '.description // ""')
  remaining=$(echo "$data" | jq -r '.todos_remaining_count // 0')
  completed_ratio=$(echo "$data" | jq -r '.completed_ratio // "0%"')

  echo "## Todolist #$id"
  echo
  echo "**$name**"
  echo
  md_kv "Remaining" "$remaining todos" \
        "Completed" "$completed_ratio"

  if [[ -n "$description" ]]; then
    echo "### Description"
    echo "$description"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}


_help_todolists() {
  cat <<'EOF'
## bcq todolists

List and show todolists in a project.

### Usage

    bcq todolists [action] [options]

### Actions

    list              List todolists (default)
    show <id>         Show todolist details

### Options

    --in, -p <project>    Project ID

### Examples

    # List todolists
    bcq todolists --in 12345

    # Show todolist details
    bcq todolists show 67890 --project 12345

    # List todos in a specific todolist
    bcq todos --list 67890 --in 12345

EOF
}
