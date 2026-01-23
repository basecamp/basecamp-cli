#!/usr/bin/env bash
# show.sh - Show any recording by ID


cmd_show() {
  local type="" id="" project=""
  local positionals=()

  # Parse all arguments in single pass
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --type|-t)
        [[ -z "${2:-}" ]] && die "--type requires a value" $EXIT_USAGE
        type="$2"
        shift 2
        ;;
      --help|-h)
        _help_show
        return
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq show --help"
        ;;
      *)
        # Collect positional arguments
        positionals+=("$1")
        shift
        ;;
    esac
  done

  # Process positionals: [type] <id>
  if [[ ${#positionals[@]} -eq 1 ]]; then
    # Single positional - must be ID
    id="${positionals[0]}"
  elif [[ ${#positionals[@]} -ge 2 ]]; then
    # Two positionals - type and ID
    type="${positionals[0]}"
    id="${positionals[1]}"
  fi

  if [[ -z "$id" ]]; then
    die "Recording ID required" $EXIT_USAGE "Usage: bcq show [type] <id> [--project <id>]"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Determine endpoint based on type
  local endpoint
  case "$type" in
    todo|todos)
      endpoint="/buckets/$project/todos/$id.json"
      ;;
    todolist|todolists)
      endpoint="/buckets/$project/todolists/$id.json"
      ;;
    message|messages)
      endpoint="/buckets/$project/messages/$id.json"
      ;;
    comment|comments)
      endpoint="/buckets/$project/comments/$id.json"
      ;;
    card|cards)
      endpoint="/buckets/$project/card_tables/cards/$id.json"
      ;;
    card-table|card_table|cardtable)
      endpoint="/buckets/$project/card_tables/$id.json"
      ;;
    document|documents)
      endpoint="/buckets/$project/documents/$id.json"
      ;;
    ""|recording|recordings)
      # Generic recording lookup
      endpoint="/buckets/$project/recordings/$id.json"
      ;;
    *)
      die "Unknown type: $type" $EXIT_USAGE "Supported: todo, todolist, message, comment, card, card-table, document"
      ;;
  esac

  local response
  response=$(api_get "$endpoint")

  local title
  title=$(echo "$response" | jq -r '.title // .name // .content // .subject // "Item"' | head -c 60)
  local item_type
  item_type=$(echo "$response" | jq -r '.type // "Recording"')
  local summary="$item_type #$id: $title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "comment" "bcq comment \"text\" --on $id" "Add comment")"
  )

  output "$response" "$summary" "$bcs" "_show_md"
}


_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id type title content creator created_at
  id=$(echo "$data" | jq -r '.id')
  type=$(echo "$data" | jq -r '.type // "Recording"')
  title=$(echo "$data" | jq -r '.title // .name // .subject // ""')
  content=$(echo "$data" | jq -r '.content // .description // ""')
  creator=$(echo "$data" | jq -r '.creator.name // "Unknown"')
  created_at=$(echo "$data" | jq -r '.created_at // ""')

  echo "## $type #$id"
  echo
  if [[ -n "$title" ]]; then
    echo "**$title**"
    echo
  fi

  md_kv "Type" "$type" \
        "Created by" "$creator" \
        "Created" "${created_at:0:10}"

  if [[ -n "$content" ]]; then
    echo "### Content"
    # Strip HTML tags for display
    echo "$content" | sed 's/<[^>]*>//g'
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}


_help_show() {
  cat <<'EOF'
## bcq show

Show details of any Basecamp recording by ID.

### Usage

    bcq show [type] <id> [--project <id>]

### Types

    todo, todolist, message, comment, card, card-table, document

If no type specified, uses generic recording lookup.

### Options

    --project, -p <id>    Project ID
    --type, -t <type>     Recording type

### Examples

    # Show a todo
    bcq show todo 12345 --project 67890

    # Show any recording (auto-detect type)
    bcq show 12345 --project 67890

    # With default project configured
    bcq show todo 12345

EOF
}
