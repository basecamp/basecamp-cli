#!/usr/bin/env bash
# cards.sh - Card Table commands

cmd_cards() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _cards_list "$@"
    return
  fi

  shift || true

  case "$action" in
    list) _cards_list "$@" ;;
    get|show) _cards_show "$@" ;;
    create) _cards_create "$@" ;;
    move) _cards_move "$@" ;;
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
        project="$2"
        shift 2
        ;;
      --column|-c)
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

  # Get columns (lists) in the card table
  local columns_response
  columns_response=$(api_get "/buckets/$project/card_tables/$card_table_id/columns.json")

  # Get cards from all columns or specific column
  local all_cards="[]"
  if [[ -n "$column" ]]; then
    # Get cards from specific column
    local column_id
    column_id=$(echo "$columns_response" | jq -r --arg name "$column" '.[] | select(.title == $name) | .id // empty')
    if [[ -z "$column_id" ]]; then
      die "Column '$column' not found" $EXIT_NOT_FOUND
    fi
    all_cards=$(api_get "/buckets/$project/card_tables/columns/$column_id/cards.json" 2>/dev/null || echo '[]')
  else
    # Get cards from all columns
    while IFS= read -r col_id; do
      [[ -z "$col_id" ]] && continue
      local cards
      cards=$(api_get "/buckets/$project/card_tables/columns/$col_id/cards.json" 2>/dev/null || echo '[]')
      all_cards=$(echo "$all_cards" "$cards" | jq -s '.[0] + .[1]')
    done < <(echo "$columns_response" | jq -r '.[].id')
  fi

  local count
  count=$(echo "$all_cards" | jq 'length')
  local summary="$count cards"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq card create \"title\" --in $project" "Create card")" \
    "$(breadcrumb "show" "bcq cards <id>" "Show card details")" \
    "$(breadcrumb "columns" "bcq columns --in $project" "List columns")"
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
        project="$2"
        shift 2
        ;;
      --column|-c)
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

  local column_id
  if [[ -n "$column" ]]; then
    # Find column by name
    local columns
    columns=$(api_get "/buckets/$project/card_tables/$card_table_id/columns.json")
    column_id=$(echo "$columns" | jq -r --arg name "$column" '.[] | select(.title == $name) | .id // empty')
    if [[ -z "$column_id" ]]; then
      die "Column '$column' not found" $EXIT_NOT_FOUND
    fi
  else
    # Use first column
    local columns
    columns=$(api_get "/buckets/$project/card_tables/$card_table_id/columns.json")
    column_id=$(echo "$columns" | jq -r '.[0].id // empty')
    if [[ -z "$column_id" ]]; then
      die "No columns found in card table" $EXIT_NOT_FOUND
    fi
  fi

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  local response
  response=$(api_post "/buckets/$project/card_tables/columns/$column_id/cards.json" "$payload")

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
        target_column="$2"
        shift 2
        ;;
      --project|-p)
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

  local columns column_id
  columns=$(api_get "/buckets/$project/card_tables/$card_table_id/columns.json")
  column_id=$(echo "$columns" | jq -r --arg name "$target_column" '.[] | select(.title == $name) | .id // empty')

  if [[ -z "$column_id" ]]; then
    die "Column '$target_column' not found" $EXIT_NOT_FOUND
  fi

  # Move card to column
  local payload
  payload=$(jq -n --arg column_id "$column_id" '{column_id: ($column_id | tonumber)}')

  local response
  response=$(api_put "/buckets/$project/card_tables/cards/$card_id.json" "$payload")

  local summary="✓ Moved card #$card_id to '$target_column'"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "view" "bcq cards $card_id" "View card")" \
    "$(breadcrumb "list" "bcq cards --in $project --column \"$target_column\"" "List cards in column")"
  )

  output "$response" "$summary" "$bcs"
}

# Shortcut for creating cards
cmd_card() {
  local action="${1:-}"

  if [[ "$action" == "create" ]]; then
    shift
    _cards_create "$@"
  elif [[ "$action" == "move" ]]; then
    shift
    _cards_move "$@"
  elif [[ "$action" =~ ^[0-9]+$ ]]; then
    _cards_show "$@"
  else
    # Assume it's a title for create
    _cards_create "$@"
  fi
}

_help_cards() {
  cat <<'EOF'
## bcq cards

Manage cards in Card Tables (Kanban boards).

### Usage

    bcq cards [action] [options]
    bcq card "title" [options]    # Shortcut for create

### Actions

    list              List cards (default)
    show <id>         Show card details
    create "title"    Create a new card
    move <id>         Move card to another column

### Options

    --in, -p <project>    Project ID
    --column, -c <name>   Filter by or target column

### Examples

    # List all cards in project
    bcq cards --in 12345

    # List cards in specific column
    bcq cards --in 12345 --column "In Progress"

    # Create card
    bcq card "New feature" --in 12345
    bcq card "Bug fix" --in 12345 --column "Inbox"

    # Move card
    bcq cards move 67890 --to "Done"

    # Show card details
    bcq cards 67890

EOF
}
