#!/usr/bin/env bash
# assign.sh - Assign todos to people


cmd_assign() {
  local todo_id=""
  local assignee=""
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --to|-a)
        [[ -z "${2:-}" ]] && die "--to requires a person (ID, email, or 'me')" $EXIT_USAGE
        assignee="$2"
        shift 2
        ;;
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_assign
        return
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
    die "Todo ID required" $EXIT_USAGE "Usage: bcq assign <todo_id> --to <person>"
  fi

  if [[ -z "$assignee" ]]; then
    die "Assignee required" $EXIT_USAGE "Usage: bcq assign <todo_id> --to <person>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  # Resolve assignee to ID
  local assignee_id
  assignee_id=$(resolve_assignee "$assignee")

  if [[ -z "$assignee_id" ]] || [[ "$assignee_id" == "null" ]]; then
    die "Invalid assignee: $assignee" $EXIT_USAGE \
      "Use numeric person ID or 'me'"
  fi

  # Get current todo to preserve existing assignees
  local current_todo current_assignees
  current_todo=$(api_get "/buckets/$project/todos/$todo_id.json")
  current_assignees=$(echo "$current_todo" | jq '[.assignees[]?.id]')

  # Add new assignee if not already assigned
  local new_assignees
  new_assignees=$(echo "$current_assignees" | jq --argjson new "$assignee_id" '. + [$new] | unique')

  # Update todo with new assignees
  local payload
  payload=$(jq -n --argjson ids "$new_assignees" '{assignee_ids: $ids}')

  local response
  response=$(api_put "/buckets/$project/todos/$todo_id.json" "$payload")

  # Get assignee name for display
  local assignee_name
  assignee_name=$(echo "$response" | jq -r --argjson id "$assignee_id" '.assignees[] | select(.id == $id) | .name // "Unknown"')

  local summary="✓ Assigned todo #$todo_id to $assignee_name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq show todo $todo_id --project $project" "View todo")" \
    "$(breadcrumb "unassign" "bcq unassign $todo_id --from $assignee_id" "Remove assignee")"
  )

  output "$response" "$summary" "$bcs"
}


cmd_unassign() {
  local todo_id=""
  local assignee=""
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --from|-a)
        [[ -z "${2:-}" ]] && die "--from requires a person (ID, email, or 'me')" $EXIT_USAGE
        assignee="$2"
        shift 2
        ;;
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_assign
        return
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
    die "Todo ID required" $EXIT_USAGE "Usage: bcq unassign <todo_id> --from <person>"
  fi

  if [[ -z "$assignee" ]]; then
    die "Assignee required" $EXIT_USAGE "Usage: bcq unassign <todo_id> --from <person>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Resolve assignee to ID
  local assignee_id
  assignee_id=$(resolve_assignee "$assignee")

  if [[ -z "$assignee_id" ]]; then
    die "Invalid assignee: $assignee" $EXIT_USAGE "Use numeric person ID or 'me'"
  fi

  # Get current todo
  local current_todo current_assignees
  current_todo=$(api_get "/buckets/$project/todos/$todo_id.json")
  current_assignees=$(echo "$current_todo" | jq '[.assignees[]?.id]')

  # Remove assignee
  local new_assignees
  new_assignees=$(echo "$current_assignees" | jq --argjson rm "$assignee_id" '[.[] | select(. != $rm)]')

  # Update todo
  local payload
  payload=$(jq -n --argjson ids "$new_assignees" '{assignee_ids: $ids}')

  local response
  response=$(api_put "/buckets/$project/todos/$todo_id.json" "$payload")

  local summary="✓ Removed assignee from todo #$todo_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq show todo $todo_id --project $project" "View todo")" \
    "$(breadcrumb "assign" "bcq assign $todo_id --to <person>" "Add assignee")"
  )

  output "$response" "$summary" "$bcs"
}


_help_assign() {
  cat <<'EOF'
## bcq assign / unassign

Assign or unassign people from todos.

### Usage

    bcq assign <todo_id> --to <person> [--project <id>]
    bcq unassign <todo_id> --from <person> [--project <id>]

### Person Resolution

- `me` - Current authenticated user
- `12345` - Person ID
- `user@example.com` - Email address (looks up ID)

### Options

    --to, -a <person>      Person to assign
    --from, -a <person>    Person to unassign
    --project, -p <id>     Project ID

### Examples

    # Assign to yourself
    bcq assign 12345 --to me

    # Assign by ID
    bcq assign 12345 --to 67890

    # Unassign
    bcq unassign 12345 --from me

EOF
}
