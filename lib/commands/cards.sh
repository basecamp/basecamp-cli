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
    column) _cards_column "$@" ;;
    create) _cards_create "$@" ;;
    show) _cards_show "$@" ;;
    list) _cards_list "$@" ;;
    move) _cards_move "$@" ;;
    steps) _cards_steps "$@" ;;
    step) _cards_step "$@" ;;
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

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local all_cards="[]"

  # If column is a numeric ID, use it directly (skip card table discovery)
  if [[ -n "$column" ]] && [[ "$column" =~ ^[0-9]+$ ]]; then
    all_cards=$(api_get "/buckets/$project/card_tables/lists/$column/cards.json" 2>/dev/null || echo '[]')
  else
    # Discover card table from project dock
    local project_data card_table_id
    project_data=$(api_get "/projects/$project.json")
    card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project")

    local card_table_data columns_response
    card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
    columns_response=$(echo "$card_table_data" | jq '.lists // []')

    if [[ -n "$column" ]]; then
      # Get cards from specific column by name
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
  fi

  local count
  count=$(echo "$all_cards" | jq 'length')
  local summary="$count cards"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq card --title \"title\" --in $project" "Create card")" \
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
  local project="" card_table=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --card-table)
        [[ -z "${2:-}" ]] && die "--card-table requires a value" $EXIT_USAGE
        card_table="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get project data and resolve card table
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project" --id "$card_table")

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
    "$(breadcrumb "create" "bcq card --title \"title\" --in $project --column <id>" "Create card in column")"
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

# Column management sub-command
_cards_column() {
  local action="${1:-}"

  case "$action" in
    show) shift; _cards_column_show "$@" ;;
    create) shift; _cards_column_create "$@" ;;
    update) shift; _cards_column_update "$@" ;;
    move) shift; _cards_column_move "$@" ;;
    watch) shift; _cards_column_watch "$@" ;;
    unwatch) shift; _cards_column_unwatch "$@" ;;
    on-hold) shift; _cards_column_on_hold "$@" ;;
    no-on-hold) shift; _cards_column_no_on_hold "$@" ;;
    color) shift; _cards_column_color "$@" ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _cards_column_show "$@"
      else
        die "Unknown column action: $action" $EXIT_USAGE "Actions: show, create, update, move, watch, unwatch, on-hold, no-on-hold, color"
      fi
      ;;
  esac
}

# GET /buckets/:bucket/card_tables/columns/:id.json
_cards_column_show() {
  local column_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/card_tables/columns/$column_id.json")

  local title cards_count
  title=$(echo "$response" | jq -r '.title // "Column"')
  cards_count=$(echo "$response" | jq -r '.cards_count // 0')
  local summary="$title ($cards_count cards)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "cards" "bcq cards --in $project --column $column_id" "List cards in column")" \
    "$(breadcrumb "update" "bcq cards column update $column_id --project $project" "Update column")" \
    "$(breadcrumb "columns" "bcq cards columns --in $project" "List all columns")"
  )

  output "$response" "$summary" "$bcs" "_cards_column_show_md"
}

_cards_column_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title description color cards_count
  title=$(echo "$data" | jq -r '.title // "Column"')
  description=$(echo "$data" | jq -r '.description // ""')
  color=$(echo "$data" | jq -r '.color // "none"')
  cards_count=$(echo "$data" | jq -r '.cards_count // 0')

  echo "## Column: $title"
  echo
  echo "**Cards**: $cards_count"
  echo "**Color**: $color"
  if [[ -n "$description" ]] && [[ "$description" != "null" ]]; then
    echo "**Description**: $description"
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/card_tables/:card_table/columns.json
_cards_column_create() {
  local title="" description="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --description|-d)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
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
    die "Column title required" $EXIT_USAGE "Usage: bcq cards column create \"title\" --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Discover card table from project dock
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project")

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')
  [[ -n "$description" ]] && payload=$(echo "$payload" | jq --arg d "$description" '. + {description: $d}')

  local response
  response=$(api_post "/buckets/$project/card_tables/$card_table_id/columns.json" "$payload")

  local column_id
  column_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created column #$column_id: $title"

  output "$response" "$summary"
}

# PUT /buckets/:bucket/card_tables/columns/:id.json
_cards_column_update() {
  local column_id="" title="" description="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --description|-d)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column update <id> --title \"new title\""
  fi

  if [[ -z "$title" ]] && [[ -z "$description" ]]; then
    die "No update fields provided" $EXIT_USAGE "Use --title or --description"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload="{}"
  [[ -n "$title" ]] && payload=$(echo "$payload" | jq --arg t "$title" '. + {title: $t}')
  [[ -n "$description" ]] && payload=$(echo "$payload" | jq --arg d "$description" '. + {description: $d}')

  local response
  response=$(api_put "/buckets/$project/card_tables/columns/$column_id.json" "$payload")

  local summary="✓ Updated column #$column_id"

  output "$response" "$summary"
}

# POST /buckets/:bucket/card_tables/:card_table/moves.json (for columns)
_cards_column_move() {
  local column_id="" position="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --position|--pos)
        [[ -z "${2:-}" ]] && die "--position requires a value" $EXIT_USAGE
        position="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column move <id> --position <n>"
  fi

  if [[ -z "$position" ]]; then
    die "--position required" $EXIT_USAGE "Specify target position (1-indexed)"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Discover card table from project dock
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project")

  local payload
  payload=$(jq -n \
    --argjson source "$column_id" \
    --argjson target "$card_table_id" \
    --argjson position "$position" \
    '{source_id: $source, target_id: $target, position: $position}')

  api_post "/buckets/$project/card_tables/$card_table_id/moves.json" "$payload" >/dev/null

  local summary="✓ Moved column #$column_id to position $position"

  output '{}' "$summary"
}

# POST /buckets/:bucket/card_tables/lists/:id/subscription.json
_cards_column_watch() {
  local column_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column watch <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  api_post "/buckets/$project/card_tables/lists/$column_id/subscription.json" '{}' >/dev/null

  local summary="✓ Now watching column #$column_id"

  output '{}' "$summary"
}

# DELETE /buckets/:bucket/card_tables/lists/:id/subscription.json
_cards_column_unwatch() {
  local column_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column unwatch <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  api_delete "/buckets/$project/card_tables/lists/$column_id/subscription.json" >/dev/null

  local summary="✓ Stopped watching column #$column_id"

  output '{}' "$summary"
}

# POST /buckets/:bucket/card_tables/columns/:id/on_hold.json
_cards_column_on_hold() {
  local column_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column on-hold <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_post "/buckets/$project/card_tables/columns/$column_id/on_hold.json" '{}')

  local summary="✓ Enabled on-hold section for column #$column_id"

  output "$response" "$summary"
}

# DELETE /buckets/:bucket/card_tables/columns/:id/on_hold.json
_cards_column_no_on_hold() {
  local column_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column no-on-hold <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_delete "/buckets/$project/card_tables/columns/$column_id/on_hold.json")

  local summary="✓ Disabled on-hold section for column #$column_id"

  output "$response" "$summary"
}

# PUT /buckets/:bucket/card_tables/columns/:id/color.json
_cards_column_color() {
  local column_id="" color="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --color|-c)
        [[ -z "${2:-}" ]] && die "--color requires a value" $EXIT_USAGE
        color="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$column_id" ]]; then
          column_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$column_id" ]]; then
    die "Column ID required" $EXIT_USAGE "Usage: bcq cards column color <id> --color <color>"
  fi

  if [[ -z "$color" ]]; then
    die "--color required" $EXIT_USAGE "Colors: white, red, orange, yellow, green, blue, aqua, purple, gray, pink, brown"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --arg c "$color" '{color: $c}')

  local response
  response=$(api_put "/buckets/$project/card_tables/columns/$column_id/color.json" "$payload")

  local summary="✓ Set column #$column_id color to $color"

  output "$response" "$summary"
}

# Step management sub-command
_cards_step() {
  local action="${1:-}"

  case "$action" in
    create) shift; _cards_step_create "$@" ;;
    update) shift; _cards_step_update "$@" ;;
    complete) shift; _cards_step_complete "$@" ;;
    uncomplete) shift; _cards_step_uncomplete "$@" ;;
    move) shift; _cards_step_move "$@" ;;
    *)
      die "Unknown step action: $action" $EXIT_USAGE "Actions: create, update, complete, uncomplete, move"
      ;;
  esac
}

# Steps are returned as part of card, so _cards_steps just shows card with step info
_cards_steps() {
  local card_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --card|-c)
        [[ -z "${2:-}" ]] && die "--card requires a value" $EXIT_USAGE
        card_id="$2"
        shift 2
        ;;
      --in|--project|-p)
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
    die "Card ID required" $EXIT_USAGE "Usage: bcq cards steps <card_id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/card_tables/cards/$card_id.json")

  local steps
  steps=$(echo "$response" | jq '.steps // []')
  local count
  count=$(echo "$steps" | jq 'length')
  local summary="$count steps on card #$card_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "create" "bcq cards step create \"title\" --card $card_id --project $project" "Add step")" \
    "$(breadcrumb "card" "bcq cards $card_id --project $project" "View card")"
  )

  output "$steps" "$summary" "$bcs" "_cards_steps_md"
}

_cards_steps_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Card Steps ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No steps*"
  else
    echo "| # | Step | Due | Assignees | Done |"
    echo "|---|------|-----|-----------|------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title // "-" | .[0:35]) | \(.due_on // "-") | \([.assignees[]?.name] | join(", ") | if . == "" then "-" else . end) | \(.completed // false) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/card_tables/cards/:card/steps.json
_cards_step_create() {
  local title="" card_id="" due="" assignees="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --card|-c)
        [[ -z "${2:-}" ]] && die "--card requires a value" $EXIT_USAGE
        card_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --due|-d)
        [[ -z "${2:-}" ]] && die "--due requires a value" $EXIT_USAGE
        due="$2"
        shift 2
        ;;
      --assignees|-a)
        [[ -z "${2:-}" ]] && die "--assignees requires a value" $EXIT_USAGE
        assignees="$2"
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
    die "Step title required" $EXIT_USAGE "Usage: bcq cards step create \"title\" --card <card_id>"
  fi

  if [[ -z "$card_id" ]]; then
    die "--card required" $EXIT_USAGE "Specify the card ID"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  if [[ -n "$due" ]]; then
    local due_date
    due_date=$(parse_date "$due")
    payload=$(echo "$payload" | jq --arg d "$due_date" '. + {due_on: $d}')
  fi

  [[ -n "$assignees" ]] && payload=$(echo "$payload" | jq --arg a "$assignees" '. + {assignees: $a}')

  local response
  response=$(api_post "/buckets/$project/card_tables/cards/$card_id/steps.json" "$payload")

  local step_id
  step_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created step #$step_id: $title"

  output "$response" "$summary"
}

# PUT /buckets/:bucket/card_tables/steps/:id.json
_cards_step_update() {
  local step_id="" title="" due="" assignees="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --due|-d)
        [[ -z "${2:-}" ]] && die "--due requires a value" $EXIT_USAGE
        due="$2"
        shift 2
        ;;
      --assignees|-a)
        [[ -z "${2:-}" ]] && die "--assignees requires a value" $EXIT_USAGE
        assignees="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$step_id" ]]; then
          step_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$step_id" ]]; then
    die "Step ID required" $EXIT_USAGE "Usage: bcq cards step update <id> --title \"new title\""
  fi

  if [[ -z "$title" ]] && [[ -z "$due" ]] && [[ -z "$assignees" ]]; then
    die "No update fields provided" $EXIT_USAGE "Use --title, --due, or --assignees"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload="{}"
  [[ -n "$title" ]] && payload=$(echo "$payload" | jq --arg t "$title" '. + {title: $t}')

  if [[ -n "$due" ]]; then
    local due_date
    due_date=$(parse_date "$due")
    payload=$(echo "$payload" | jq --arg d "$due_date" '. + {due_on: $d}')
  fi

  [[ -n "$assignees" ]] && payload=$(echo "$payload" | jq --arg a "$assignees" '. + {assignees: $a}')

  local response
  response=$(api_put "/buckets/$project/card_tables/steps/$step_id.json" "$payload")

  local summary="✓ Updated step #$step_id"

  output "$response" "$summary"
}

# PUT /buckets/:bucket/card_tables/steps/:id/completions.json
_cards_step_complete() {
  local step_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$step_id" ]]; then
          step_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$step_id" ]]; then
    die "Step ID required" $EXIT_USAGE "Usage: bcq cards step complete <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload='{"completion": "on"}'

  local response
  response=$(api_put "/buckets/$project/card_tables/steps/$step_id/completions.json" "$payload")

  local summary="✓ Completed step #$step_id"

  output "$response" "$summary"
}

# PUT /buckets/:bucket/card_tables/steps/:id/completions.json (uncomplete)
_cards_step_uncomplete() {
  local step_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$step_id" ]]; then
          step_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$step_id" ]]; then
    die "Step ID required" $EXIT_USAGE "Usage: bcq cards step uncomplete <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload='{"completion": "off"}'

  local response
  response=$(api_put "/buckets/$project/card_tables/steps/$step_id/completions.json" "$payload")

  local summary="✓ Uncompleted step #$step_id"

  output "$response" "$summary"
}

# POST /buckets/:bucket/card_tables/cards/:card/positions.json
_cards_step_move() {
  local step_id="" card_id="" position="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --card|-c)
        [[ -z "${2:-}" ]] && die "--card requires a value" $EXIT_USAGE
        card_id="$2"
        shift 2
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --position|--pos)
        [[ -z "${2:-}" ]] && die "--position requires a value" $EXIT_USAGE
        position="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$step_id" ]]; then
          step_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$step_id" ]]; then
    die "Step ID required" $EXIT_USAGE "Usage: bcq cards step move <step_id> --card <card_id> --position <n>"
  fi

  if [[ -z "$card_id" ]]; then
    die "--card required" $EXIT_USAGE "Specify the card ID"
  fi

  if [[ -z "$position" ]]; then
    die "--position required" $EXIT_USAGE "Specify target position (0-indexed)"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n \
    --argjson source "$step_id" \
    --argjson position "$position" \
    '{source_id: $source, position: $position}')

  api_post "/buckets/$project/card_tables/cards/$card_id/positions.json" "$payload" >/dev/null

  local summary="✓ Moved step #$step_id to position $position"

  output '{}' "$summary"
}

_cards_show() {
  local card_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --help|-h)
        echo "Usage: bcq cards show <id> --project <project_id>"
        return
        ;;
      -*)
        shift
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
    die "Card ID required" $EXIT_USAGE "Usage: bcq cards show <id> --project <project_id>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

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
  local title="" content="" project="" column=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_card_create
        return
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
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq card --help"
        ;;
      *)
        die "Unexpected argument: $1" $EXIT_USAGE "Run: bcq card --help"
        ;;
    esac
  done

  if [[ -z "$title" ]]; then
    die "Card title required" $EXIT_USAGE "Usage: bcq card --title \"title\" --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local column_id

  # If column is a numeric ID, use it directly (skip card table discovery)
  if [[ -n "$column" ]] && [[ "$column" =~ ^[0-9]+$ ]]; then
    column_id="$column"
  else
    # Discover card table from project dock
    local project_data card_table_id
    project_data=$(api_get "/projects/$project.json")
    card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project")

    local card_table_data columns
    card_table_data=$(api_get "/buckets/$project/card_tables/$card_table_id.json")
    columns=$(echo "$card_table_data" | jq '.lists // []')

    if [[ -n "$column" ]]; then
      # Find column by name
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
  fi

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')
  [[ -n "$content" ]] && payload=$(echo "$payload" | jq --arg c "$content" '. + {content: $c}')

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

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Discover card table from project dock
  local project_data card_table_id
  project_data=$(api_get "/projects/$project.json")
  card_table_id=$(require_dock_tool "$project_data" "kanban_board" "$project")

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

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

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

_help_card_create() {
  cat << 'EOF'
bcq card - Create a new card

USAGE
  bcq card --title "title" [options]

OPTIONS
  --title, -t <text>        Card title (required)
  --content, -b <text>      Card body/description (HTML supported)
  --in, --project, -p <id>  Project ID or name
  --column, -c <id|name>    Column ID or name (defaults to first column)

EXAMPLES
  bcq card --title "New feature" --in 123
  bcq card -t "Bug fix" --content "Details about the bug" --in "My Project"
  bcq card -t "Task" -b "<strong>Priority:</strong> High" --column "In Progress" --in 123
EOF
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
    bcq card --title "title" [options]    # Shortcut for create

### Card Actions

    columns           List columns with stable IDs
    create "title"    Create a new card
    list              List cards (default)
    move <id>         Move card to another column
    show <id>         Show card details
    update <id>       Update card attributes

### Column Actions (bcq cards column <action>)

    show <id>         Show column details
    create "title"    Create a new column
    update <id>       Update column title/description
    move <id>         Reorder column position
    watch <id>        Start watching column
    unwatch <id>      Stop watching column
    on-hold <id>      Enable on-hold section
    no-on-hold <id>   Disable on-hold section
    color <id>        Change column color

### Step Actions (bcq cards step <action>)

    create "title"    Create step on a card
    update <id>       Update step
    complete <id>     Mark step complete
    uncomplete <id>   Mark step incomplete
    move <id>         Reposition step

### Options

    --in, -p <project>      Project ID
    --column, -c <id|name>  Filter by or target column (ID preferred for stability)
    --card <id>             Card ID (for step operations)
    --title, -t <text>      Title (for update)
    --content, -b <text>    Card description (for update)
    --due, -d <date>        Due date (for update)
    --assignee, -a <id>     Assignee ID or 'me' (for update)
    --color <color>         Column color (white, red, orange, yellow, green, blue, aqua, purple, gray, pink, brown)
    --position <n>          Position for move operations

### Examples

    # List all cards in project
    bcq cards --in 12345

    # Create card with body content
    bcq card --title "New feature" --content "Details here" --in 12345

    # Column operations
    bcq cards column create "In Progress" --in 12345
    bcq cards column color 67890 --color blue --in 12345

    # Step operations
    bcq cards steps 12345 --in 67890           # List steps on card
    bcq cards step create "Do this" --card 12345 --in 67890
    bcq cards step complete 111 --in 67890

EOF
}
