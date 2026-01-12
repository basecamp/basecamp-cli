#!/usr/bin/env bash
# comment.sh - Add comments to recordings (todos, messages, etc.)

cmd_comment() {
  local content=""
  local recording_id=""
  local project=""

  # Parse arguments
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --on|-r)
        [[ -z "${2:-}" ]] && die "--on requires a recording ID" $EXIT_USAGE
        recording_id="$2"
        shift 2
        ;;
      --project|-p)
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
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$content" ]]; then
    die "Comment content required" $EXIT_USAGE \
      "Usage: bcq comment \"content\" --on <recording_id>"
  fi

  if [[ -z "$recording_id" ]]; then
    die "Recording ID required" $EXIT_USAGE \
      "Usage: bcq comment \"content\" --on <recording_id>"
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

  local response
  response=$(api_post "/buckets/$project/recordings/$recording_id/comments.json" "$payload")

  local comment_id
  comment_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Added comment #$comment_id to recording #$recording_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq show comment $comment_id --project $project" "View comment")" \
    "$(breadcrumb "recording" "bcq show $recording_id --project $project" "View recording")"
  )

  output "$response" "$summary" "$bcs"
}

_help_comment() {
  cat <<'EOF'
## bcq comment

Add a comment to any Basecamp recording (todo, message, etc.)

### Usage

    bcq comment "content" --on <recording_id> [--project <id>]

### Examples

    # Comment on a todo
    bcq comment "Fixed in commit abc123" --on 12345

    # With explicit project
    bcq comment "Done!" --on 12345 --project 67890

    # Link a PR to a todo
    bcq comment "PR: https://github.com/org/repo/pull/42" --on 12345

### Options

    --on, -r <id>       Recording ID to comment on (required)
    --project, -p <id>  Project ID (uses config default if not set)

EOF
}
