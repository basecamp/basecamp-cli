#!/usr/bin/env bash
# comment.sh - Comment commands (list, show, create, update)

# Main comments command (list/show/update)
cmd_comments() {
  local action="${1:-}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _comments_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _comments_list "$@" ;;
    show) _comments_show "$@" ;;
    update) _comments_update "$@" ;;
    --help|-h) _help_comments ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _comments_show "$action" "$@"
      else
        die "Unknown comments action: $action" $EXIT_USAGE "Run: bcq comments --help"
      fi
      ;;
  esac
}

_comments_list() {
  local recording_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --on|-r)
        [[ -z "${2:-}" ]] && die "--on requires a recording ID" $EXIT_USAGE
        recording_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_comments
        return
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
    die "Recording ID required" $EXIT_USAGE "Usage: bcq comments --on <recording_id>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local response
  response=$(api_get "/buckets/$project/recordings/$recording_id/comments.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count comments on recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "add" "bcq comment \"text\" --on $recording_id --in $project" "Add comment")" \
    "$(breadcrumb "show" "bcq comments <id> --in $project" "Show comment")"
  )

  output "$response" "$summary" "$bcs" "_comments_list_md"
}

_comments_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Comments ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No comments*"
  else
    echo "| # | Author | Content | Date |"
    echo "|---|--------|---------|------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.creator.name) | \(.content | gsub("<[^>]*>"; "") | gsub("\n"; " ") | .[0:40]) | \(.created_at | split("T")[0]) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_comments_show() {
  local comment_id="${1:-}"
  shift || true
  local project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  if [[ -z "$comment_id" ]]; then
    die "Comment ID required" $EXIT_USAGE "Usage: bcq comments show <id> --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local response
  response=$(api_get "/buckets/$project/comments/$comment_id.json")

  local creator
  creator=$(echo "$response" | jq -r '.creator.name // "Unknown"')
  local summary="Comment #$comment_id by $creator"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "update" "bcq comments update $comment_id --in $project" "Update comment")" \
    "$(breadcrumb "recording" "bcq show \$(jq -r '.parent.id') --in $project" "View parent")"
  )

  output "$response" "$summary" "$bcs" "_comments_show_md"
}

_comments_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id creator content created_at
  id=$(echo "$data" | jq -r '.id')
  creator=$(echo "$data" | jq -r '.creator.name // "Unknown"')
  content=$(echo "$data" | jq -r '.content // ""')
  created_at=$(echo "$data" | jq -r '.created_at | split("T")[0]')

  echo "## Comment #$id"
  echo
  md_kv "Author" "$creator" \
        "Created" "$created_at"
  echo "### Content"
  echo "$content" | sed 's/<[^>]*>//g'
  echo
  md_breadcrumbs "$breadcrumbs"
}

_comments_update() {
  local comment_id="" content="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --content|-c)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$comment_id" ]]; then
          comment_id="$1"
        elif [[ -z "$content" ]] && [[ "$1" != -* ]]; then
          content="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$comment_id" ]]; then
    die "Comment ID required" $EXIT_USAGE "Usage: bcq comments update <id> \"content\" --in <project>"
  fi

  if [[ -z "$content" ]]; then
    die "Content required" $EXIT_USAGE "Usage: bcq comments update <id> \"content\" --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  local payload
  payload=$(jq -n --arg content "$content" '{content: $content}')

  local response
  response=$(api_put "/buckets/$project/comments/$comment_id.json" "$payload")

  local summary="Updated comment #$comment_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq comments $comment_id --in $project" "View comment")"
  )

  output "$response" "$summary" "$bcs"
}

_help_comments() {
  cat <<'EOF'
## bcq comments

List, show, and update comments on recordings.

### Usage

    bcq comments [action] [options]

### Actions

    list          List comments on a recording (default)
    show <id>     Show comment details
    update <id>   Update comment content

### Options

    --on <id>         Recording ID to list comments for
    --in, -p <id>     Project ID
    --content <text>  New content (for update)

### Examples

    # List comments on a todo
    bcq comments --on 12345 --in 67890

    # Show a specific comment
    bcq comments show 11111 --in 67890

    # Update a comment
    bcq comments update 11111 "New content" --in 67890

### See Also

    bcq comment "text" --on <id>   Add a new comment

EOF
}


# Shortcut: bcq comment "text" --on <id> [id...] (create only, supports batch)
cmd_comment() {
  local content=""
  local recording_ids=()
  local project=""
  local from_stdin=""

  # Parse arguments
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --on|-r)
        [[ -z "${2:-}" ]] && die "--on requires a recording ID" $EXIT_USAGE
        # Support comma-separated IDs
        IFS=',' read -ra ids <<< "$2"
        recording_ids+=("${ids[@]}")
        shift 2
        ;;
      --ids)
        # Read IDs from stdin or argument
        if [[ "${2:-}" == "@-" ]] || [[ "${2:-}" == "-" ]]; then
          from_stdin="true"
          shift 2
        else
          [[ -z "${2:-}" ]] && die "--ids requires IDs or @- for stdin" $EXIT_USAGE
          IFS=',' read -ra ids <<< "$2"
          recording_ids+=("${ids[@]}")
          shift 2
        fi
        ;;
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        _help_comment
        return
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$content" ]]; then
          content="$1"
        elif [[ "$1" =~ ^[0-9]+$ ]]; then
          recording_ids+=("$1")
        fi
        shift
        ;;
    esac
  done

  # Read IDs from stdin if requested
  if [[ "$from_stdin" == "true" ]]; then
    while IFS= read -r line; do
      [[ -z "$line" ]] && continue
      # Support one ID per line or comma-separated
      IFS=',' read -ra ids <<< "$line"
      for id in "${ids[@]}"; do
        id=$(echo "$id" | tr -d '[:space:]')
        [[ "$id" =~ ^[0-9]+$ ]] && recording_ids+=("$id")
      done
    done
  fi

  if [[ -z "$content" ]]; then
    die "Comment content required" $EXIT_USAGE \
      "Usage: bcq comment \"content\" --on <id> [id...]"
  fi

  if [[ ${#recording_ids[@]} -eq 0 ]]; then
    die "Recording ID(s) required" $EXIT_USAGE \
      "Usage: bcq comment \"content\" --on <id> [id...]"
  fi

  # Get project from context if not specified
  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE \
      "Use --project or set in .basecamp/config.json"
  fi

  # Build payload
  local payload
  payload=$(jq -n --arg content "$content" '{content: $content}')

  # Batch comment on all recording IDs
  local commented=()
  local comment_ids=()
  local failed=()
  for recording_id in "${recording_ids[@]}"; do
    local response
    # Capture stdout only; let stderr flow through for verbose output
    if response=$(api_post "/buckets/$project/recordings/$recording_id/comments.json" "$payload"); then
      commented+=("$recording_id")
      comment_ids+=("$(echo "$response" | jq -r '.id')")
    else
      failed+=("$recording_id")
    fi
  done

  local count=${#commented[@]}
  local fail_count=${#failed[@]}
  local summary
  if ((fail_count > 0)); then
    summary="⚠ Added $count comment(s), $fail_count failed: ${failed[*]}"
  else
    summary="✓ Added $count comment(s) to: ${commented[*]}"
  fi

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq todos --in $project" "List todos")"
  )

  local result
  result=$(jq -n \
    --argjson recordings "$(printf '%s\n' "${commented[@]}" | jq -R . | jq -s .)" \
    --argjson comments "$(printf '%s\n' "${comment_ids[@]}" | jq -R . | jq -s .)" \
    --argjson failed "$(printf '%s\n' "${failed[@]}" | jq -R . | jq -s .)" \
    '{commented_recordings: $recordings, comment_ids: $comments, failed: $failed}')

  output "$result" "$summary" "$bcs"

  # Exit with error if any failed
  ((fail_count > 0)) && return 1
  return 0
}

_help_comment() {
  cat <<'EOF'
## bcq comment

Add a comment to one or more Basecamp recordings (todos, messages, etc.)

### Usage

    bcq comment "content" --on <id> [id...] [--project <id>]
    bcq comment "content" --on id1,id2,id3 [--project <id>]
    echo "123 456 789" | bcq comment "content" --ids @-

### Examples

    # Comment on a single todo
    bcq comment "Fixed in commit abc123" --on 12345

    # Comment on multiple todos at once (batch)
    bcq comment "Processed in sweep" --on 111 222 333

    # Comma-separated IDs
    bcq comment "Done!" --on 111,222,333 --project 67890

    # Read IDs from stdin (useful with jq)
    bcq todos --overdue --json | jq -r '.data[].id' | bcq comment "Swept" --ids @-

    # Link a PR to a todo
    bcq comment "PR: https://github.com/org/repo/pull/42" --on 12345

### Options

    --on, -r <id...>     Recording ID(s) to comment on (required)
    --ids <ids|@->       IDs as comma-separated list or @- for stdin
    --project, -p <id>   Project ID (uses config default if not set)

EOF
}
