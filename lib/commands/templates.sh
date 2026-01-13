#!/usr/bin/env bash
# templates.sh - Project template management
# Covers templates.md (7 endpoints)

cmd_templates() {
  local action="${1:-}"

  case "$action" in
    list|ls) shift; _templates_list "$@" ;;
    show|get) shift; _template_show "$@" ;;
    create|new) shift; _template_create "$@" ;;
    update) shift; _template_update "$@" ;;
    delete|trash) shift; _template_delete "$@" ;;
    construct|build) shift; _template_construct "$@" ;;
    construction) shift; _template_construction_status "$@" ;;
    --help|-h) _help_templates ;;
    "")
      _templates_list "$@"
      ;;
    -*)
      # Flags go to list
      _templates_list "$@"
      ;;
    *)
      # If numeric, treat as template ID to show
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _template_show "$@"
      else
        die "Unknown templates action: $action" $EXIT_USAGE "Run: bcq templates --help"
      fi
      ;;
  esac
}

# GET /templates.json
_templates_list() {
  local status="active"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --status)
        [[ -z "${2:-}" ]] && die "--status requires a value" $EXIT_USAGE
        status="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  local response
  response=$(api_get "/templates.json?status=$status")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count templates"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq templates show <id>" "View template details")" \
    "$(breadcrumb "create" "bcq templates create \"Name\"" "Create new template")" \
    "$(breadcrumb "construct" "bcq templates construct <id> --name \"Project Name\"" "Create project from template")"
  )

  output "$response" "$summary" "$bcs" "_templates_list_md"
}

_templates_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Templates ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No templates found*"
  else
    echo "| ID | Name | Description | Status |"
    echo "|----|------|-------------|--------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.name | .[0:30]) | \(.description // "-" | .[0:40]) | \(.status) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /templates/:id.json
_template_show() {
  local template_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$template_id" ]]; then
          template_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$template_id" ]]; then
    die "Template ID required" $EXIT_USAGE "Usage: bcq templates show <id>"
  fi

  local response
  response=$(api_get "/templates/$template_id.json")

  local name
  name=$(echo "$response" | jq -r '.name // "Template"')
  local summary="$name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "construct" "bcq templates construct $template_id --name \"Project Name\"" "Create project from template")" \
    "$(breadcrumb "update" "bcq templates update $template_id --name \"New Name\"" "Update template")" \
    "$(breadcrumb "list" "bcq templates" "List all templates")"
  )

  output "$response" "$summary" "$bcs" "_template_show_md"
}

_template_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local name desc dock_count
  name=$(echo "$data" | jq -r '.name // "Template"')
  desc=$(echo "$data" | jq -r '.description // ""')
  dock_count=$(echo "$data" | jq -r '.dock | length')

  echo "## $name"
  echo
  if [[ -n "$desc" ]] && [[ "$desc" != "null" ]]; then
    echo "$desc"
    echo
  fi

  echo "| Property | Value |"
  echo "|----------|-------|"
  echo "$data" | jq -r '"| ID | \(.id) |"'
  echo "$data" | jq -r '"| Status | \(.status) |"'
  echo "$data" | jq -r '"| Created | \(.created_at | split("T")[0]) |"'
  echo "| Dock Items | $dock_count |"
  echo

  # Show enabled dock items
  local enabled_docks
  enabled_docks=$(echo "$data" | jq '[.dock[] | select(.enabled == true) | .title] | join(", ")')
  if [[ -n "$enabled_docks" ]] && [[ "$enabled_docks" != '""' ]]; then
    echo "**Enabled Tools**: $enabled_docks"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

# POST /templates.json
_template_create() {
  local name="" description=""

  # First positional arg is name if not a flag
  if [[ $# -gt 0 ]] && [[ ! "$1" =~ ^- ]]; then
    name="$1"
    shift
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --description|--desc)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      *)
        if [[ -z "$name" ]] && [[ ! "$1" =~ ^- ]]; then
          name="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$name" ]]; then
    die "Template name required" $EXIT_USAGE "Usage: bcq templates create \"Template Name\""
  fi

  local payload
  payload=$(jq -n --arg name "$name" '{name: $name}')

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg desc "$description" '. + {description: $desc}')
  fi

  local response
  response=$(api_post "/templates.json" "$payload")

  local template_id
  template_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created template #$template_id: $name"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq templates show $template_id" "View template")" \
    "$(breadcrumb "construct" "bcq templates construct $template_id --name \"Project Name\"" "Create project from template")"
  )

  output "$response" "$summary" "$bcs"
}

# PUT /templates/:id.json
_template_update() {
  local template_id="" name="" description=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        name="$2"
        shift 2
        ;;
      --description|--desc)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$template_id" ]]; then
          template_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$template_id" ]]; then
    die "Template ID required" $EXIT_USAGE "Usage: bcq templates update <id> [options]"
  fi

  local payload="{}"

  if [[ -n "$name" ]]; then
    payload=$(echo "$payload" | jq --arg v "$name" '. + {name: $v}')
  fi

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg v "$description" '. + {description: $v}')
  fi

  if [[ "$payload" == "{}" ]]; then
    die "No update fields provided" $EXIT_USAGE "Use --name or --description"
  fi

  local response
  response=$(api_put "/templates/$template_id.json" "$payload")

  local summary="✓ Updated template #$template_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq templates show $template_id" "View template")"
  )

  output "$response" "$summary" "$bcs"
}

# DELETE /templates/:id.json
_template_delete() {
  local template_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$template_id" ]]; then
          template_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$template_id" ]]; then
    die "Template ID required" $EXIT_USAGE "Usage: bcq templates delete <id>"
  fi

  api_delete "/templates/$template_id.json"

  local summary="✓ Trashed template #$template_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq templates" "List templates")" \
    "$(breadcrumb "archived" "bcq templates --status trashed" "View trashed templates")"
  )

  json_success "$summary" "$bcs"
}

# POST /templates/:id/project_constructions.json
_template_construct() {
  local template_id="" project_name="" project_description=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        project_name="$2"
        shift 2
        ;;
      --description|--desc)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        project_description="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$template_id" ]]; then
          template_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$template_id" ]]; then
    die "Template ID required" $EXIT_USAGE "Usage: bcq templates construct <template_id> --name \"Project Name\""
  fi

  if [[ -z "$project_name" ]]; then
    die "--name required (project name)" $EXIT_USAGE
  fi

  local payload
  payload=$(jq -n --arg name "$project_name" '{project: {name: $name}}')

  if [[ -n "$project_description" ]]; then
    payload=$(echo "$payload" | jq --arg desc "$project_description" '.project.description = $desc')
  fi

  local response
  response=$(api_post "/templates/$template_id/project_constructions.json" "$payload")

  local construction_id status
  construction_id=$(echo "$response" | jq -r '.id')
  status=$(echo "$response" | jq -r '.status')
  local summary="✓ Started project construction #$construction_id ($status)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "status" "bcq templates construction $template_id $construction_id" "Check construction status")"
  )

  output "$response" "$summary" "$bcs"
}

# GET /templates/:id/project_constructions/:id.json
_template_construction_status() {
  local template_id="" construction_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      *)
        if [[ "$1" =~ ^[0-9]+$ ]]; then
          if [[ -z "$template_id" ]]; then
            template_id="$1"
          elif [[ -z "$construction_id" ]]; then
            construction_id="$1"
          fi
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$template_id" ]]; then
    die "Template ID required" $EXIT_USAGE "Usage: bcq templates construction <template_id> <construction_id>"
  fi

  if [[ -z "$construction_id" ]]; then
    die "Construction ID required" $EXIT_USAGE "Usage: bcq templates construction <template_id> <construction_id>"
  fi

  local response
  response=$(api_get "/templates/$template_id/project_constructions/$construction_id.json")

  local status project_id project_name
  status=$(echo "$response" | jq -r '.status')

  if [[ "$status" == "completed" ]]; then
    project_id=$(echo "$response" | jq -r '.project.id')
    project_name=$(echo "$response" | jq -r '.project.name')
    local summary="Construction complete: $project_name (project #$project_id)"

    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "project" "bcq projects show $project_id" "View created project")"
    )
  else
    local summary="Construction status: $status"
    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "poll" "bcq templates construction $template_id $construction_id" "Check again")"
    )
  fi

  output "$response" "$summary" "$bcs" "_template_construction_md"
}

_template_construction_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local status
  status=$(echo "$data" | jq -r '.status')

  echo "## Project Construction"
  echo
  echo "**Status**: $status"

  if [[ "$status" == "completed" ]]; then
    echo
    echo "### Created Project"
    echo
    echo "| Property | Value |"
    echo "|----------|-------|"
    echo "$data" | jq -r '.project | "| ID | \(.id) |"'
    echo "$data" | jq -r '.project | "| Name | \(.name) |"'
    echo "$data" | jq -r '.project | "| Status | \(.status) |"'
  elif [[ "$status" == "pending" ]]; then
    echo
    echo "*Construction in progress. Poll this endpoint to check completion.*"
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

_help_templates() {
  cat <<'EOF'
## bcq templates

Manage project templates.

### Usage

    bcq templates [options]                      List templates
    bcq templates show <id>                      Show template details
    bcq templates create <name> [options]        Create new template
    bcq templates update <id> [options]          Update template
    bcq templates delete <id>                    Trash template
    bcq templates construct <id> --name <name>   Create project from template
    bcq templates construction <tid> <cid>       Check construction status

### Options

    --status <status>         Filter: active, archived, trashed (default: active)
    --name <name>             Template or project name
    --description <text>      Template or project description

### Examples

    # List all templates
    bcq templates

    # View a template
    bcq templates show 123

    # Create a template
    bcq templates create "Sprint Template" --description "Standard sprint setup"

    # Update a template
    bcq templates update 123 --name "Updated Name"

    # Create a project from template
    bcq templates construct 123 --name "Q1 Sprint" --description "First quarter"

    # Check construction status (poll until completed)
    bcq templates construction 123 456

    # Delete (trash) a template
    bcq templates delete 123

### Notes

Creating a project from a template is asynchronous. The `construct` command
returns a construction ID which can be polled via `construction` until
status is "completed". When complete, the response includes the new project.

EOF
}
