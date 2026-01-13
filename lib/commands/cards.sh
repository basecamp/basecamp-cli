#!/usr/bin/env bash
# cards.sh - Card Table commands

# Resolve column by ID or name. IDs are stable; names can change.
# Usage: resolve_column "$columns_json" "$id_or_name"
# Returns: column ID or empty string
_resolve_column() {
  local columns="$1"
  local identifier="$2"

  # If it looks like an ID (numeric), try direct match first
  if [[ "$identifier" =~ ^[0-9]+$ ]]; then
    local by_id
    by_id=$(echo "$columns" | jq -r --arg id "$identifier" '.[] | select(.id == ($id | tonumber)) | .id // empty')
    if [[ -n "$by_id" ]]; then
      echo "$by_id"
      return
    fi
  fi

  # Fall back to name match
  echo "$columns" | jq -r --arg name "$identifier" '.[] | select(.title == $name) | .id // empty'
}

cmd_cards() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _cards_list "$@"
    return
  fi

  shift || true

  case "$action" in
    columns) _cards_columns "$@" ;;
    create) _cards_create "$@" ;;
    get|show) _cards_show "$@" ;;
    list) _cards_list "$@" ;;
    move) _cards_move "$@" ;;
    update) _cards_update "$@" ;;
    --help|-h) _help_cards ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _cards_show "$action" "$@"
      else
        die "Unknown cards action: $action" $EXIT_USAGE "Run: bcq cards --help"
      fi
      ;;
  esac
}

_cards_list() {
  local project="" column=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --column|-c)
        [[ -z "${2:-}" ]] && die "--column requires a value" $EXIT_USAGE
        column="$2"
        shift 2
        ;;
      --help|-h)
        _help_cards
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

  # Get card table from project dock
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "kanban_board") | .id // empty')

  if [[ -z "$card_table_id" ]]; then
    die "No card table found in project $project" $EXIT_NOT_FOUND
  fi

  # Get card table with embedded columns (lists)
  local card_table_data columns_response
  card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
  columns_response=$(echo "$card_table_data" | jq '.lists // []')

  # Get cards from all columns or specific column
  local all_cards="[]"
  if [[ -n "$column" ]]; then
    # Get cards from specific column (accepts ID or name)
    local column_id
    column_id=$(_resolve_column "$columns_response" "$column")
    if [[ -z "$column_id" ]]; then
      die "Column '$column' not found" $EXIT_NOT_FOUND "Use column ID or exact name"
    fi
    all_cards=$(api_get "/buckets/$project/card_tables/lists/$column_id/cards.json" 2>/dev/null || echo '[]')
  else
    # Get cards from all columns
    while IFS= read -r col_id; do
      [[ -z "$col_id" ]] && continue
      local cards
      cards=$(api_get "/buckets/$project/card_tables/lists/$col_id/cards.json" 2>/dev/null || echo '[]')
      all_cards=$(echo "$all_cards" "$cards" | jq -s '.[0] + .[1]')
    done < <(echo "$columns_response" | jq -r '.[].id')
  fi

  local count
  count=$(echo "$all_cards" | jq 'length')
  local summary="$count cards"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq card \"title\" --in $project" "Create card")" \
    "$(breadcrumb "show" "bcq cards <id>" "Show card details")" \
    "$(breadcrumb "columns" "bcq cards columns --in $project" "List columns with IDs")"
  )

  output "$all_cards" "$summary" "$bcs" "_cards_list_md"
}

_cards_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Cards ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No cards found*"
  else
    echo "| # | Title | Column | Assignees |"
    echo "|---|-------|--------|-----------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title // .content | .[0:40]) | \(.parent.title // "-") | \([.assignees[]?.name] | join(", ") | if . == "" then "-" else . end) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_cards_columns() {
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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified. Use --in <project>" $EXIT_USAGE
  fi

  # Get card table from project dock
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "kanban_board") | .id // empty')

  if [[ -z "$card_table_id" ]]; then
    die "No card table found in project $project" $EXIT_NOT_FOUND
  fi

  # Get card table with embedded columns (lists)
  local card_table_data columns
  card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
  columns=$(echo "$card_table_data" | jq '.lists // []')

  local count
  count=$(echo "$columns" | jq 'length')
  local summary="$count columns"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "cards" "bcq cards --in $project --column <id>" "List cards in column")" \
    "$(breadcrumb "create" "bcq card \"title\" --in $project --column <id>" "Create card in column")"
  )

  output "$columns" "$summary" "$bcs" "_cards_columns_md"
}

_cards_columns_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Card Table Columns ($summary)"
  echo
  echo "| ID | Name | Cards |"
  echo "|----|------|-------|"
  echo "$data" | jq -r '.[] | "| \(.id) | \(.title) | \(.cards_count // "-") |"'
  echo
  echo "*Use column ID for stability (names can change)*"
  echo
  md_breadcrumbs "$breadcrumbs"
}

_cards_show() {
  local card_id="$1"
  local project="${2:-$(get_project_id)}"

  if [[ -z "$card_id" ]]; then
    die "Card ID required" $EXIT_USAGE "Usage: bcq cards show <id>"
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local response
  response=$(api_get "/buckets/$project/card_tables/cards/$card_id.json")

  local title
  title=$(echo "$response" | jq -r '.title // .content')
  local summary="Card #$card_id: $title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "move" "bcq cards move $card_id --to \"Column\"" "Move card")" \
    "$(breadcrumb "comment" "bcq comment \"text\" --on $card_id" "Add comment")" \
    "$(breadcrumb "list" "bcq cards --in $project" "List cards")"
  )

  output "$response" "$summary" "$bcs" "_cards_show_md"
}

_cards_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id title content column assignees due_on
  id=$(echo "$data" | jq -r '.id')
  title=$(echo "$data" | jq -r '.title // .content')
  content=$(echo "$data" | jq -r '.content // ""')
  column=$(echo "$data" | jq -r '.parent.title // "Unknown"')
  assignees=$(echo "$data" | jq -r '[.assignees[]?.name] | join(", ") | if . == "" then "Unassigned" else . end')
  due_on=$(echo "$data" | jq -r '.due_on // "Not set"')

  echo "## Card #$id"
  echo
  echo "**$title**"
  echo
  md_kv "Column" "$column" \
        "Assignees" "$assignees" \
        "Due" "$due_on"

  if [[ -n "$content" ]] && [[ "$content" != "$title" ]]; then
    echo "### Description"
    echo "$content"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

_cards_create() {
  local title="" project="" column=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --column|-c)
        [[ -z "${2:-}" ]] && die "--column requires a value" $EXIT_USAGE
        column="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$title" ]]; then
          title="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$title" ]]; then
    die "Card title required" $EXIT_USAGE "Usage: bcq card create \"title\" --in <project>"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --in <project>"
  fi

  # Get card table and first column if not specified
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "kanban_board") | .id // empty')

  if [[ -z "$card_table_id" ]]; then
    die "No card table found in project $project" $EXIT_NOT_FOUND
  fi

  # Get card table with embedded columns (lists)
  local card_table_data columns
  card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
  columns=$(echo "$card_table_data" | jq '.lists // []')

  local column_id
  if [[ -n "$column" ]]; then
    # Find column by ID or name
    column_id=$(_resolve_column "$columns" "$column")
    if [[ -z "$column_id" ]]; then
      die "Column '$column' not found" $EXIT_NOT_FOUND "Use column ID or exact name"
    fi
  else
    # Use first column (Inbox/Triage)
    column_id=$(echo "$columns" | jq -r '.[0].id // empty')
    if [[ -z "$column_id" ]]; then
      die "No columns found in card table" $EXIT_NOT_FOUND
    fi
  fi

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  local response
  response=$(api_post "/buckets/$project/card_tables/lists/$column_id/cards.json" "$payload")

  local card_id
  card_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created card #$card_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq cards $card_id --project $project" "View card")" \
    "$(breadcrumb "move" "bcq cards move $card_id --to \"Column\"" "Move card")" \
    "$(breadcrumb "list" "bcq cards --in $project" "List cards")"
  )

  output "$response" "$summary" "$bcs"
}

_cards_move() {
  local card_id="" project="" target_column=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --to|-t)
        [[ -z "${2:-}" ]] && die "--to requires a column name" $EXIT_USAGE
        target_column="$2"
        shift 2
        ;;
      --project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$card_id" ]]; then
          card_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$card_id" ]]; then
    die "Card ID required" $EXIT_USAGE "Usage: bcq cards move <id> --to \"Column\""
  fi

  if [[ -z "$target_column" ]]; then
    die "Target column required" $EXIT_USAGE "Usage: bcq cards move <id> --to \"Column\""
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

  # Get card table and find target column
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "kanban_board") | .id // empty')

  # Get card table with embedded columns (lists)
  local card_table_data columns column_id
  card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
  columns=$(echo "$card_table_data" | jq '.lists // []')
  column_id=$(_resolve_column "$columns" "$target_column")

  if [[ -z "$column_id" ]]; then
    die "Column '$target_column' not found" $EXIT_NOT_FOUND "Use column ID or exact name"
  fi

  # Move card to column via moves endpoint
  local payload
  payload=$(jq -n --arg column_id "$column_id" '{column_id: ($column_id | tonumber)}')

  # POST to /moves.json returns 204 No Content on success
  api_post "/buckets/$project/card_tables/cards/$card_id/moves.json" "$payload" >/dev/null

  local summary="✓ Moved card #$card_id to '$target_column'"
  local response='{}'

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq cards $card_id" "View card")" \
    "$(breadcrumb "list" "bcq cards --in $project --column \"$target_column\"" "List cards in column")"
  )

  output "$response" "$summary" "$bcs"
}

_cards_update() {
  local card_id="" project="" title="" content="" due="" assignees=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|-p|--in)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      --due|-d)
        [[ -z "${2:-}" ]] && die "--due requires a value" $EXIT_USAGE
        due="$2"
        shift 2
        ;;
      --assignee|-a)
        [[ -z "${2:-}" ]] && die "--assignee requires a value" $EXIT_USAGE
        assignees="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$card_id" ]]; then
          card_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$card_id" ]]; then
    die "Card ID required" $EXIT_USAGE "Usage: bcq cards update <id> --title \"new title\""
  fi

  if [[ -z "$title" ]] && [[ -z "$content" ]] && [[ -z "$due" ]] && [[ -z "$assignees" ]]; then
    die "At least one field required" $EXIT_USAGE "Use --title, --content, --due, or --assignee"
  fi

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project or set in .basecamp/config.json"
  fi

  local payload="{}"
  [[ -n "$title" ]] && payload=$(echo "$payload" | jq --arg t "$title" '. + {title: $t}')
  [[ -n "$content" ]] && payload=$(echo "$payload" | jq --arg c "$content" '. + {content: $c}')

  if [[ -n "$due" ]]; then
    local due_date
    due_date=$(parse_date "$due")
    payload=$(echo "$payload" | jq --arg d "$due_date" '. + {due_on: $d}')
  fi

  if [[ -n "$assignees" ]]; then
    local assignee_id
    assignee_id=$(resolve_assignee "$assignees")
    if [[ -z "$assignee_id" ]]; then
      die "Invalid assignee: $assignees" $EXIT_USAGE "Use numeric person ID or 'me'"
    fi
    payload=$(echo "$payload" | jq --argjson ids "[$assignee_id]" '. + {assignee_ids: $ids}')
  fi

  local response
  response=$(api_put "/buckets/$project/card_tables/cards/$card_id.json" "$payload")

  local summary="Updated card #$card_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq cards $card_id --in $project" "View card")" \
    "$(breadcrumb "list" "bcq cards --in $project" "List cards")"
  )

  output "$response" "$summary" "$bcs"
}

# Shortcut for creating cards
cmd_card() {
  local action="${1:-}"

  case "$action" in
    create)
      shift
      _cards_create "$@"
      ;;
    move)
      shift
      _cards_move "$@"
      ;;
    update)
      shift
      _cards_update "$@"
      ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _cards_show "$@"
      else
        # Assume it's a title for create
        _cards_create "$@"
      fi
      ;;
  esac
}

_help_cards() {
  cat <<'EOF'
## bcq cards

Manage cards in Card Tables (Kanban boards).

### Usage

    bcq cards [action] [options]
    bcq card "title" [options]    # Shortcut for create

### Actions

    columns           List columns with stable IDs
    create "title"    Create a new card
    list              List cards (default)
    move <id>         Move card to another column
    show <id>         Show card details
    update <id>       Update card attributes

### Options

    --in, -p <project>      Project ID
    --column, -c <id|name>  Filter by or target column (ID preferred for stability)
    --title, -t <text>      Card title (for update)
    --content, -b <text>    Card description (for update)
    --due, -d <date>        Due date (for update)
    --assignee, -a <id>     Assignee ID or 'me' (for update)

### Examples

    # List all cards in project
    bcq cards --in 12345

    # List cards in specific column
    bcq cards --in 12345 --column "In Progress"

    # Create card
    bcq card "New feature" --in 12345
    bcq card "Bug fix" --in 12345 --column "Inbox"

    # Update card
    bcq cards update 67890 --title "Updated title" --due tomorrow

    # Move card
    bcq cards move 67890 --to "Done"

    # Show card details
    bcq cards 67890

EOF
}
