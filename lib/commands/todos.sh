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
    uncomplete|reopen) cmd_todo_uncomplete "$@" ;;
    position|reorder|move) _todos_position "$@" ;;
    sweep) _todos_sweep "$@" ;;
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
  local project="" todolist="" assignee="" status="" overdue=""

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
      --overdue)
        overdue="true"
        shift
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

    # Get todolists in the todoset (with pagination)
    local todolists_response
    todolists_response=$(api_get_all "/buckets/$project/todosets/$todoset/todolists.json")

    # Aggregate todos from all todolists (with pagination)
    local all_todos="[]"
    while IFS= read -r tl_id; do
      [[ -z "$tl_id" ]] && continue
      local todos
      todos=$(api_get_all "/buckets/$project/todolists/$tl_id/todos.json" 2>/dev/null || echo '[]')
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
      if [[ -z "$assignee_id" ]]; then
        die "Invalid assignee: $assignee" $EXIT_USAGE "Use numeric person ID or 'me'"
      fi
      all_todos=$(echo "$all_todos" | jq --arg assignee "$assignee_id" '[.[] | select(.assignees[]?.id == ($assignee | tonumber))]')
    fi

    # Filter by overdue
    if [[ "$overdue" == "true" ]]; then
      local today
      today=$(date +%Y-%m-%d)
      all_todos=$(echo "$all_todos" | jq --arg today "$today" '[.[] | select(.due_on != null and .due_on < $today and .completed == false)]')
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

  # Direct todolist query - fetch all pages
  local response
  response=$(api_get_all "$path")

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
    if [[ -z "$assignee_id" ]]; then
      die "Invalid assignee: $assignee" $EXIT_USAGE "Use numeric person ID or 'me'"
    fi
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
      --in|--project|-p)
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
    die "Todo ID(s) required" $EXIT_USAGE "Usage: bcq done <id> [id...] --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project> or set in .basecamp/config.json"
  fi

  local completed=()
  local failed=()
  for todo_id in "${todo_ids[@]}"; do
    if api_post "/buckets/$project/todos/$todo_id/completion.json" "" >/dev/null 2>&1; then
      completed+=("$todo_id")
    else
      failed+=("$todo_id")
    fi
  done

  local count=${#completed[@]}
  local fail_count=${#failed[@]}
  local summary
  if ((fail_count > 0)); then
    summary="⚠ Completed $count todo(s), $fail_count failed: ${failed[*]}"
  else
    summary="✓ Completed $count todo(s): ${completed[*]}"
  fi

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todos --in $project" "List remaining todos")" \
    "$(breadcrumb "reopen" "bcq reopen ${completed[0]:-}" "Reopen a todo")"
  )

  local result
  result=$(jq -n \
    --argjson completed "$(printf '%s\n' "${completed[@]}" | jq -R . | jq -s .)" \
    --argjson failed "$(printf '%s\n' "${failed[@]}" | jq -R . | jq -s .)" \
    '{completed: $completed, failed: $failed}')

  output "$result" "$summary" "$bcs"

  ((fail_count > 0)) && return 1
  return 0
}


cmd_todo_uncomplete() {
  local todo_ids=()
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
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
    die "Todo ID(s) required" $EXIT_USAGE "Usage: bcq reopen <id> [id...]"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local reopened=()
  local failed=()
  for todo_id in "${todo_ids[@]}"; do
    if api_delete "/buckets/$project/todos/$todo_id/completion.json" >/dev/null 2>&1; then
      reopened+=("$todo_id")
    else
      failed+=("$todo_id")
    fi
  done

  local count=${#reopened[@]}
  local fail_count=${#failed[@]}
  local summary
  if ((fail_count > 0)); then
    summary="⚠ Reopened $count todo(s), $fail_count failed: ${failed[*]}"
  else
    summary="↺ Reopened $count todo(s): ${reopened[*]}"
  fi

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todos --in $project" "List todos")" \
    "$(breadcrumb "complete" "bcq done ${reopened[0]:-}" "Complete again")"
  )

  local result
  result=$(jq -n \
    --argjson reopened "$(printf '%s\n' "${reopened[@]}" | jq -R . | jq -s .)" \
    --argjson failed "$(printf '%s\n' "${failed[@]}" | jq -R . | jq -s .)" \
    '{reopened: $reopened, failed: $failed}')

  output "$result" "$summary" "$bcs"

  ((fail_count > 0)) && return 1
  return 0
}


# Workflow command: find + comment + complete in one atomic operation
_todos_sweep() {
  local project="" comment="" complete="false" dry_run="false"
  local filter_overdue="false" filter_assignee="" filter_status=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --overdue)
        filter_overdue="true"
        shift
        ;;
      --assignee|-a)
        [[ -z "${2:-}" ]] && die "--assignee requires a value" $EXIT_USAGE
        filter_assignee="$2"
        shift 2
        ;;
      --comment|-c)
        [[ -z "${2:-}" ]] && die "--comment requires a value" $EXIT_USAGE
        comment="$2"
        shift 2
        ;;
      --complete|--done)
        complete="true"
        shift
        ;;
      --dry-run|-n)
        dry_run="true"
        shift
        ;;
      --help|-h)
        _help_todos_sweep
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Require at least one filter
  if [[ "$filter_overdue" != "true" ]] && [[ -z "$filter_assignee" ]]; then
    die "Sweep requires a filter" $EXIT_USAGE \
      "Use --overdue or --assignee to select todos"
  fi

  # Require at least one action
  if [[ -z "$comment" ]] && [[ "$complete" != "true" ]]; then
    die "Sweep requires an action" $EXIT_USAGE \
      "Use --comment and/or --complete"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  # Build list args
  local list_args=(--in "$project")
  [[ "$filter_overdue" == "true" ]] && list_args+=(--overdue)
  [[ -n "$filter_assignee" ]] && list_args+=(--assignee "$filter_assignee")

  # Get matching todos (reuse existing list logic)
  local todos_json
  todos_json=$(_todos_list_json "${list_args[@]}")

  local count
  count=$(echo "$todos_json" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    local result='{"swept": [], "count": 0}'
    output "$result" "No todos match the filter" ""
    return
  fi

  # Extract IDs
  local todo_ids=()
  while IFS= read -r id; do
    [[ -n "$id" ]] && todo_ids+=("$id")
  done < <(echo "$todos_json" | jq -r '.[].id')

  if [[ "$dry_run" == "true" ]]; then
    local result
    result=$(jq -n \
      --argjson ids "$(printf '%s\n' "${todo_ids[@]}" | jq -R . | jq -s .)" \
      --argjson count "$count" \
      --arg comment "$comment" \
      --arg complete "$complete" \
      '{dry_run: true, would_sweep: $ids, count: $count, comment: $comment, complete: ($complete == "true")}')
    output "$result" "Would sweep $count todo(s)" ""
    return
  fi

  # Execute actions
  local swept=()
  local commented=()
  local completed=()
  local comment_failed=()
  local complete_failed=()

  for todo_id in "${todo_ids[@]}"; do
    swept+=("$todo_id")

    # Add comment if specified
    if [[ -n "$comment" ]]; then
      local payload
      payload=$(jq -n --arg content "$comment" '{content: $content}')
      if api_post "/buckets/$project/recordings/$todo_id/comments.json" "$payload" >/dev/null 2>&1; then
        commented+=("$todo_id")
      else
        comment_failed+=("$todo_id")
      fi
    fi

    # Complete if specified
    if [[ "$complete" == "true" ]]; then
      if api_post "/buckets/$project/todos/$todo_id/completion.json" "" >/dev/null 2>&1; then
        completed+=("$todo_id")
      else
        complete_failed+=("$todo_id")
      fi
    fi
  done

  local summary="Swept ${#swept[@]} todo(s)"
  [[ ${#commented[@]} -gt 0 ]] && summary+=", commented ${#commented[@]}"
  [[ ${#completed[@]} -gt 0 ]] && summary+=", completed ${#completed[@]}"

  local has_failures="false"
  if [[ ${#comment_failed[@]} -gt 0 ]]; then
    summary+=", ${#comment_failed[@]} comment failed"
    has_failures="true"
  fi
  if [[ ${#complete_failed[@]} -gt 0 ]]; then
    summary+=", ${#complete_failed[@]} complete failed"
    has_failures="true"
  fi

  local result
  result=$(jq -n \
    --argjson swept "$(printf '%s\n' "${swept[@]}" | jq -R . | jq -s .)" \
    --argjson commented "$(printf '%s\n' "${commented[@]}" | jq -R . | jq -s .)" \
    --argjson completed "$(printf '%s\n' "${completed[@]}" | jq -R . | jq -s .)" \
    --argjson comment_failed "$(printf '%s\n' "${comment_failed[@]}" | jq -R . | jq -s .)" \
    --argjson complete_failed "$(printf '%s\n' "${complete_failed[@]}" | jq -R . | jq -s .)" \
    '{swept: $swept, commented: $commented, completed: $completed, comment_failed: $comment_failed, complete_failed: $complete_failed}')

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todos --in $project" "List remaining todos")"
  )

  if [[ "$has_failures" == "true" ]]; then
    output "$result" "⚠ $summary" "$bcs"
    return 1
  else
    output "$result" "✓ $summary" "$bcs"
  fi
}

# Internal: get todos as JSON array (for sweep to reuse filtering)
_todos_list_json() {
  local project="" todolist="" assignee="" status="" overdue=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p) project="$2"; shift 2 ;;
      --list|--todolist|-l) todolist="$2"; shift 2 ;;
      --assignee|-a) assignee="$2"; shift 2 ;;
      --status|-s) status="$2"; shift 2 ;;
      --overdue) overdue="true"; shift ;;
      *) shift ;;
    esac
  done

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  # Get todoset ID from project dock
  local project_data todoset
  project_data=$(api_get "/projects/$project.json")
  todoset=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')

  if [[ -z "$todoset" ]]; then
    echo "[]"
    return
  fi

  # Get todolists in the todoset (with pagination)
  local todolists_response
  todolists_response=$(api_get_all "/buckets/$project/todosets/$todoset/todolists.json")

  # Aggregate todos from all todolists (with pagination)
  local all_todos="[]"
  while IFS= read -r tl_id; do
    [[ -z "$tl_id" ]] && continue
    local todos
    todos=$(api_get_all "/buckets/$project/todolists/$tl_id/todos.json" 2>/dev/null || echo '[]')
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
    if [[ -n "$assignee_id" ]]; then
      all_todos=$(echo "$all_todos" | jq --arg assignee "$assignee_id" '[.[] | select(.assignees[]?.id == ($assignee | tonumber))]')
    fi
  fi

  # Filter by overdue
  if [[ "$overdue" == "true" ]]; then
    local today
    today=$(date +%Y-%m-%d)
    all_todos=$(echo "$all_todos" | jq --arg today "$today" '[.[] | select(.due_on != null and .due_on < $today and .completed == false)]')
  fi

  echo "$all_todos"
}

_help_todos_sweep() {
  cat <<'EOF'
## bcq todos sweep

Atomic workflow: find todos by filter, then comment and/or complete them.

This encodes common multi-step workflows into a single deterministic command,
eliminating pagination, filtering, and retry logic from agent workflows.

### Usage

    bcq todos sweep --overdue --in <project> --comment "text" --complete
    bcq todos sweep --assignee me --in <project> --complete

### Options

    --in, -p <id>       Project ID (required)
    --overdue           Filter: todos past due date
    --assignee <name>   Filter: todos assigned to person
    --comment <text>    Action: add this comment to each todo
    --complete          Action: mark each todo complete
    --dry-run, -n       Show what would be swept without acting

### Examples

    # Complete all overdue todos with a comment
    bcq todos sweep --overdue --in 12345 \
      --comment "Processed in sweep $(date)" \
      --complete

    # Preview what would be swept
    bcq todos sweep --overdue --in 12345 --complete --dry-run

    # Sweep todos assigned to me
    bcq todos sweep --assignee me --in 12345 --complete

### Output

Returns JSON with arrays of affected todo IDs:

    {
      "swept": [111, 222, 333],
      "commented": [111, 222, 333],
      "completed": [111, 222, 333]
    }

EOF
}

_todos_position() {
  local todo_id="" position=""
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --position|--to)
        [[ -z "${2:-}" ]] && die "--position requires a value" $EXIT_USAGE
        position="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$todo_id" ]]; then
          todo_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$todo_id" ]]; then
    die "Todo ID required" $EXIT_USAGE "Usage: bcq todos position <id> --to <position>"
  fi

  if [[ -z "$position" ]]; then
    die "Position required" $EXIT_USAGE "Use --to <position> (1 = top)"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local payload
  payload=$(jq -n --argjson pos "$position" '{position: $pos}')

  local response
  response=$(api_put "/buckets/$project/todos/$todo_id/position.json" "$payload")

  local summary="Moved todo #$todo_id to position $position"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq todos $todo_id --in $project" "View todo")" \
    "$(breadcrumb "list" "bcq todos --in $project" "List todos")"
  )

  output "${response:-'{}'}" "$summary" "$bcs"
}
