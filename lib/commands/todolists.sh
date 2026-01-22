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
    create) _todolists_create "$@" ;;
    list) _todolists_list "$@" ;;
    show) _todolists_show "$@" ;;
    update) _todolists_update "$@" ;;
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


_todolists_create() {
  local name="" project="" description=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --description|--desc|-d)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$name" ]]; then
          name="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$name" ]]; then
    die "Todolist name required" $EXIT_USAGE "Usage: bcq todolists create \"name\" --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  # Get todoset from project dock
  local project_data todoset_id
  project_data=$(api_get "/projects/$project.json")
  todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')

  if [[ -z "$todoset_id" ]]; then
    die "No todoset found in project $project" $EXIT_NOT_FOUND
  fi

  local payload
  payload=$(jq -n --arg name "$name" '{name: $name}')

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg desc "$description" '. + {description: $desc}')
  fi

  local response
  response=$(api_post "/buckets/$project/todosets/$todoset_id/todolists.json" "$payload")

  local todolist_id
  todolist_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created todolist #$todolist_id: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq todolists $todolist_id --in $project" "View todolist")" \
    "$(breadcrumb "add_todo" "bcq todo \"content\" --list $todolist_id --in $project" "Add todo")"
  )

  output "$response" "$summary" "$bcs"
}


_todolists_update() {
  local todolist_id="" project="" name="" description=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --name|-n)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --description|--desc|-d)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$todolist_id" ]]; then
          todolist_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$todolist_id" ]]; then
    die "Todolist ID required" $EXIT_USAGE "Usage: bcq todolists update <id> --name \"new name\""
  fi

  if [[ -z "$name" ]] && [[ -z "$description" ]]; then
    die "Name or description required" $EXIT_USAGE "Use --name and/or --description"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local payload="{}"
  [[ -n "$name" ]] && payload=$(echo "$payload" | jq --arg n "$name" '. + {name: $n}')
  [[ -n "$description" ]] && payload=$(echo "$payload" | jq --arg d "$description" '. + {description: $d}')

  local response
  response=$(api_put "/buckets/$project/todolists/$todolist_id.json" "$payload")

  local summary="Updated todolist #$todolist_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq todolists $todolist_id --in $project" "View todolist")"
  )

  output "$response" "$summary" "$bcs"
}


_help_todolists() {
  cat <<'EOF'
## bcq todolists

Manage todolists in a project.

NOTE: A "todoset" is the container; "todolists" are the actual lists inside it.
Each project has one todoset containing multiple todolists. Use `bcq todosets`
to see the container, use `bcq todolists` to manage the lists.

### Usage

    bcq todolists [action] [options]

### Actions

    create "name"     Create a new todolist
    list              List todolists (default)
    show <id>         Show todolist details
    update <id>       Update todolist name/description

### Options

    --in, -p <project>        Project ID
    --name, -n <name>         Todolist name (for update)
    --description, -d <text>  Todolist description

### Examples

    # List todolists
    bcq todolists --in 12345

    # Create a new todolist
    bcq todolists create "Sprint 42" --in 12345

    # Show todolist details
    bcq todolists show 67890 --in 12345

    # Update todolist
    bcq todolists update 67890 --name "Sprint 43" --in 12345

    # List todos in a specific todolist
    bcq todos --list 67890 --in 12345

EOF
}


# === Todolist Groups ===
# Groups are sub-todolists within a parent todolist.
# API: groups live under /todolists/:todolist_id/groups.json
# Groups themselves are also todolists (type: "Todolist")

cmd_todolistgroups() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _todolistgroups_list "$@"
    return
  fi

  shift || true

  case "$action" in
    create) _todolistgroups_create "$@" ;;
    list) _todolistgroups_list "$@" ;;
    position) _todolistgroups_position "$@" ;;
    show) _todolistgroups_show "$@" ;;
    update) _todolistgroups_update "$@" ;;
    --help|-h) _help_todolistgroups ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _todolistgroups_show "$action" "$@"
      else
        die "Unknown todolistgroups action: $action" $EXIT_USAGE "Run: bcq todolistgroups --help"
      fi
      ;;
  esac
}


_todolistgroups_list() {
  local project="" todolist_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --list|-l)
        [[ -z "${2:-}" ]] && die "--list requires a value" $EXIT_USAGE
        todolist_id="$2"
        shift 2
        ;;
      --help|-h)
        _help_todolistgroups
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

  if [[ -z "$todolist_id" ]]; then
    todolist_id=$(get_todolist_id)
  fi

  if [[ -z "$todolist_id" ]]; then
    die "No todolist specified. Use --list <todolist_id>" $EXIT_USAGE
  fi

  # GET /buckets/:project/todolists/:todolist_id/groups.json
  local response
  response=$(api_get "/buckets/$project/todolists/$todolist_id/groups.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count groups in todolist #$todolist_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq todolistgroups create \"name\" --list $todolist_id --in $project" "Create group")" \
    "$(breadcrumb "todolist" "bcq todolists $todolist_id --in $project" "View parent todolist")"
  )

  output "$response" "$summary" "$bcs" "_todolistgroups_list_md"
}


_todolistgroups_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Todolist Groups ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No groups found*"
  else
    echo "| # | Name | Todos | Position |"
    echo "|---|------|-------|----------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.name // .title) | \(.completed_ratio // "-") | \(.position // "-") |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}


_todolistgroups_show() {
  local group_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$group_id" ]]; then
          group_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$group_id" ]]; then
    die "Group ID required" $EXIT_USAGE "Usage: bcq todolistgroups show <id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  # Groups are todolists: GET /buckets/:project/todolists/:group_id.json
  local response
  response=$(api_get "/buckets/$project/todolists/$group_id.json")

  local name
  name=$(echo "$response" | jq -r '.name // .title')
  local summary="Todolist group: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq todolistgroups update $group_id --name \"new\" --in $project" "Update group")" \
    "$(breadcrumb "todos" "bcq todos --list $group_id --in $project" "List todos in group")"
  )

  output "$response" "$summary" "$bcs"
}


_todolistgroups_create() {
  local name="" project="" todolist_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --list|-l)
        [[ -z "${2:-}" ]] && die "--list requires a value" $EXIT_USAGE
        todolist_id="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$name" ]]; then
          name="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$name" ]]; then
    die "Group name required" $EXIT_USAGE "Usage: bcq todolistgroups create \"name\" --list <todolist_id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  if [[ -z "$todolist_id" ]]; then
    todolist_id=$(get_todolist_id)
  fi

  if [[ -z "$todolist_id" ]]; then
    die "No todolist specified. Use --list <todolist_id>" $EXIT_USAGE
  fi

  local payload
  payload=$(jq -n --arg name "$name" '{name: $name}')

  # POST /buckets/:project/todolists/:todolist_id/groups.json
  local response
  response=$(api_post "/buckets/$project/todolists/$todolist_id/groups.json" "$payload")

  local group_id
  group_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created group #$group_id: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todolistgroups --list $todolist_id --in $project" "List groups")" \
    "$(breadcrumb "todos" "bcq todos --list $group_id --in $project" "Add todos to group")"
  )

  output "$response" "$summary" "$bcs"
}


_todolistgroups_update() {
  local group_id="" project="" name=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --name|-n)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$group_id" ]]; then
          group_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$group_id" ]]; then
    die "Group ID required" $EXIT_USAGE "Usage: bcq todolistgroups update <id> --name \"new name\""
  fi

  if [[ -z "$name" ]]; then
    die "Name required" $EXIT_USAGE "Use --name \"new name\""
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local payload
  payload=$(jq -n --arg name "$name" '{name: $name}')

  # Groups are todolists: PUT /buckets/:project/todolists/:group_id.json
  local response
  response=$(api_put "/buckets/$project/todolists/$group_id.json" "$payload")

  local summary="Updated group #$group_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq todolistgroups $group_id --in $project" "View group")"
  )

  output "$response" "$summary" "$bcs"
}


_todolistgroups_position() {
  local group_id="" position="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --to|--position)
        [[ -z "${2:-}" ]] && die "--to requires a value" $EXIT_USAGE
        position="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$group_id" ]]; then
          group_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$group_id" ]]; then
    die "Group ID required" $EXIT_USAGE "Usage: bcq todolistgroups position <id> --to <position>"
  fi

  if [[ -z "$position" ]]; then
    die "Position required" $EXIT_USAGE "Use --to <position> (1 = top)"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local payload
  payload=$(jq -n --argjson pos "$position" '{position: $pos}')

  # PUT /buckets/:project/todolists/groups/:group_id/position.json
  local response
  response=$(api_put "/buckets/$project/todolists/groups/$group_id/position.json" "$payload")

  local summary="Moved group #$group_id to position $position"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq todolistgroups $group_id --in $project" "View group")"
  )

  output "${response:-'{}'}" "$summary" "$bcs"
}


_help_todolistgroups() {
  cat <<'EOF'
## bcq todolistgroups

Manage todolist groups (sub-lists within a todolist).

Groups are nested within a specific todolist and help organize todos
into categories. Groups themselves function as todolists.

### Usage

    bcq todolistgroups [action] [options]

### Actions

    create "name"     Create a new group in a todolist
    list              List groups in a todolist (default)
    position <id>     Reorder group position
    show <id>         Show group details
    update <id>       Update group name

### Options

    --in, -p <project>      Project ID
    --list, -l <todolist>   Parent todolist ID (required for list/create)
    --name, -n <name>       Group name (for update)
    --to <position>         Target position (for reorder)

### Examples

    # List groups in a todolist
    bcq todolistgroups --list 67890 --in 12345

    # Create a new group
    bcq todolistgroups create "Phase 1" --list 67890 --in 12345

    # Show group details
    bcq todolistgroups show 11111 --in 12345

    # Update group name
    bcq todolistgroups update 11111 --name "Phase 2" --in 12345

    # Move group to top
    bcq todolistgroups position 11111 --to 1 --in 12345

    # Add todos to a group (groups are todolists)
    bcq todo "Task" --list 11111 --in 12345

EOF
}
