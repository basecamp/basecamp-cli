#!/usr/bin/env bash
# todos.sh - Todo commands


cmd_todos() {
  local action="${1:-list}"

  # Check if first arg is an action or a flag
  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _todos_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _todos_list "$@" ;;
    get|show) _todos_show "$@" ;;
    create) cmd_todo_create "$@" ;;
    complete|done) cmd_todo_complete "$@" ;;
    --help|-h) _help_todos ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _todos_show "$action" "$@"
      else
        die "Unknown todos action: $action" $EXIT_USAGE "Run: bcq todos --help"
      fi
      ;;
  esac
}

_todos_list() {
  local project="" todolist="" assignee="" status=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --list|--todolist|-l)
        [[ -z "${2:-}" ]] && die "--list requires a value" $EXIT_USAGE
        todolist="$2"
        shift 2
        ;;
      --assignee|-a)
        [[ -z "${2:-}" ]] && die "--assignee requires a value" $EXIT_USAGE
        assignee="$2"
        shift 2
        ;;
      --status|-s)
        [[ -z "${2:-}" ]] && die "--status requires a value" $EXIT_USAGE
        status="$2"
        shift 2
        ;;
      --all)
        assignee=""
        shift
        ;;
      --help|-h)
        _help_todos
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Use project from context if not specified
  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified. Use --in <project> or set in .basecamp/config.json" $EXIT_USAGE
  fi

  # Build path based on context
  local path
  if [[ -n "$todolist" ]]; then
    path="/buckets/$project/todolists/$todolist/todos.json"
  else
    # Get todoset ID from project dock
    local project_data todoset
    project_data=$(api_get "/projects/$project.json")
    todoset=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')

    if [[ -z "$todoset" ]]; then
      die "No todoset found in project $project" $EXIT_NOT_FOUND
    fi

    # Get todolists in the todoset
    local todolists_response
    todolists_response=$(api_get "/buckets/$project/todosets/$todoset/todolists.json")

    # Aggregate todos from all todolists
    local all_todos="[]"
    while IFS= read -r tl_id; do
      [[ -z "$tl_id" ]] && continue
      local todos
      todos=$(api_get "/buckets/$project/todolists/$tl_id/todos.json" 2>/dev/null || echo '[]')
      all_todos=$(echo "$all_todos" "$todos" | jq -s '.[0] + .[1]')
    done < <(echo "$todolists_response" | jq -r '.[].id')

    # Filter by status
    if [[ -n "$status" ]]; then
      all_todos=$(echo "$all_todos" | jq --arg status "$status" '[.[] | select(.completed == ($status == "completed"))]')
    fi

    # Filter by assignee
    if [[ -n "$assignee" ]]; then
      local assignee_id
      assignee_id=$(resolve_assignee "$assignee")
      all_todos=$(echo "$all_todos" | jq --arg assignee "$assignee_id" '[.[] | select(.assignees[]?.id == ($assignee | tonumber))]')
    fi

    local count
    count=$(echo "$all_todos" | jq 'length')
    local summary="$count todos"

    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "create" "bcq todo \"content\" --in $project" "Create todo")" \
      "$(breadcrumb "complete" "bcq done <id>" "Complete a todo")" \
      "$(breadcrumb "show" "bcq show todo <id>" "Show todo details")"
    )

    output "$all_todos" "$summary" "$bcs" "_todos_list_md"
    return
  fi

  # Direct todolist query
  local response
  response=$(api_get "$path")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count todos"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq todo \"content\"" "Create todo")" \
    "$(breadcrumb "complete" "bcq done <id>" "Complete a todo")"
  )

  output "$response" "$summary" "$bcs" "_todos_list_md"
}

_todos_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Todos ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No todos found*"
  else
    echo "| # | Content | Due | Status |"
    echo "|---|---------|-----|--------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.content | gsub("\n"; " ") | .[0:50]) | \(.due_on // "-") | \(if .completed then "✓" else "○" end) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_todos_show() {
  local todo_id="$1"
  local project="${2:-$(get_project_id)}"

  if [[ -z "$todo_id" ]]; then
    die "Todo ID required" $EXIT_USAGE "Usage: bcq todos show <id>"
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local response
  response=$(api_get "/buckets/$project/todos/$todo_id.json")

  local content
  content=$(echo "$response" | jq -r '.content')
  local summary="Todo #$todo_id: $content"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "complete" "bcq done $todo_id" "Complete this todo")" \
    "$(breadcrumb "comment" "bcq comment \"text\" --on $todo_id" "Add comment")" \
    "$(breadcrumb "assign" "bcq assign $todo_id --to @name" "Assign todo")"
  )

  output "$response" "$summary" "$bcs" "_todos_show_md"
}

_todos_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id content description due_on completed assignees
  id=$(echo "$data" | jq -r '.id')
  content=$(echo "$data" | jq -r '.content')
  description=$(echo "$data" | jq -r '.description // ""')
  due_on=$(echo "$data" | jq -r '.due_on // "Not set"')
  completed=$(echo "$data" | jq -r '.completed')
  assignees=$(echo "$data" | jq -r '[.assignees[]?.name] | join(", ") | if . == "" then "Unassigned" else . end')

  local status_icon="○"
  [[ "$completed" == "true" ]] && status_icon="✓"

  echo "## $status_icon Todo #$id"
  echo
  echo "**$content**"
  echo
  md_kv "Status" "$([ "$completed" == "true" ] && echo "Completed" || echo "Active")" \
        "Due" "$due_on" \
        "Assignee" "$assignees"

  if [[ -n "$description" ]]; then
    echo "### Description"
    echo "$description"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}


cmd_todo_create() {
  local content="${1:-}"
  shift || true

  local project="" todolist="" assignee="" due=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        project="$2"
        shift 2
        ;;
      --list|--todolist|-l)
        todolist="$2"
        shift 2
        ;;
      --assignee|--to|-a)
        assignee="$2"
        shift 2
        ;;
      --due|-d)
        due="$2"
        shift 2
        ;;
      *)
        if [[ -z "$content" ]]; then
          content="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$content" ]]; then
    die "Todo content required" $EXIT_USAGE "Usage: bcq todo \"content\" [options]"
  fi

  # Get project from context if not specified
  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified. Use --in <project> or set in .basecamp/config.json" $EXIT_USAGE
  fi

  # Get todolist from context if not specified
  if [[ -z "$todolist" ]]; then
    todolist=$(get_config "todolist_id")
  fi

  if [[ -z "$todolist" ]]; then
    # Get first todolist from project's todoset
    local project_data todoset
    project_data=$(api_get "/projects/$project.json")
    todoset=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')
    if [[ -n "$todoset" ]]; then
      todolist=$(api_get "/buckets/$project/todosets/$todoset/todolists.json" | jq -r '.[0].id // empty')
    fi
  fi

  if [[ -z "$todolist" ]]; then
    die "No todolist found. Use --list <id>" $EXIT_USAGE
  fi

  # Build payload
  local payload
  payload=$(jq -n --arg content "$content" '{content: $content}')

  if [[ -n "$due" ]]; then
    local due_date
    due_date=$(parse_date "$due")
    payload=$(echo "$payload" | jq --arg due "$due_date" '. + {due_on: $due}')
  fi

  if [[ -n "$assignee" ]]; then
    local assignee_id
    assignee_id=$(resolve_assignee "$assignee")
    payload=$(echo "$payload" | jq --argjson ids "[$assignee_id]" '. + {assignee_ids: $ids}')
  fi

  local response
  response=$(api_post "/buckets/$project/todolists/$todolist/todos.json" "$payload")

  local todo_id
  todo_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created todo #$todo_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq show todo $todo_id --project $project" "View todo")" \
    "$(breadcrumb "complete" "bcq done $todo_id" "Complete todo")" \
    "$(breadcrumb "list" "bcq todos --in $project" "List todos")"
  )

  output "$response" "$summary" "$bcs"
}


cmd_todo_complete() {
  local todo_ids=()
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p)
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]]; then
          todo_ids+=("$1")
        fi
        shift
        ;;
    esac
  done

  if [[ ${#todo_ids[@]} -eq 0 ]]; then
    die "Todo ID(s) required" $EXIT_USAGE "Usage: bcq done <id> [id...]"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local completed=()
  for todo_id in "${todo_ids[@]}"; do
    api_post "/buckets/$project/todos/$todo_id/completion.json" "" &>/dev/null && {
      completed+=("$todo_id")
    }
  done

  local count=${#completed[@]}
  local summary="✓ Completed $count todo(s): ${completed[*]}"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todos --in $project" "List remaining todos")" \
    "$(breadcrumb "reopen" "bcq reopen ${completed[0]}" "Reopen a todo")"
  )

  local result
  result=$(jq -n --argjson ids "$(printf '%s\n' "${completed[@]}" | jq -R . | jq -s .)" '{completed: $ids}')

  output "$result" "$summary" "$bcs"
}
