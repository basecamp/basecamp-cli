#!/usr/bin/env bash
# stubs.sh - Config and MCP commands (partial implementations)


cmd_config() {
  local action="${1:-show}"
  shift || true

  case "$action" in
    show|"")
      _config_show "$@"
      ;;
    init)
      _config_init "$@"
      ;;
    set)
      _config_set "$@"
      ;;
    unset)
      _config_unset "$@"
      ;;
    project)
      _config_project "$@"
      ;;
    --help|-h)
      _help_config
      ;;
    *)
      die "Unknown config action: $action" $EXIT_USAGE "Run: bcq config --help"
      ;;
  esac
}

_config_show() {
  local format
  format=$(get_format)

  local config
  config=$(get_effective_config)

  # Build config with sources
  local config_with_sources='{}'
  local keys=("account_id" "project_id" "todolist_id" "base_url" "api_url")
  for key in "${keys[@]}"; do
    local value source
    value=$(get_config "$key")
    if [[ -n "$value" ]]; then
      source=$(get_config_source "$key")
      config_with_sources=$(echo "$config_with_sources" | jq \
        --arg key "$key" \
        --arg value "$value" \
        --arg source "$source" \
        '.[$key] = {value: $value, source: $source}')
    fi
  done

  if [[ "$format" == "json" ]]; then
    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "set" "bcq config set <key> <value>" "Set config value")" \
      "$(breadcrumb "project" "bcq config project" "Select project")"
    )
    json_ok "$config_with_sources" "Effective configuration" "$bcs"
  else
    echo "## Configuration"
    echo
    echo "| Key | Value | Source |"
    echo "|-----|-------|--------|"
    for key in "${keys[@]}"; do
      local value source
      value=$(get_config "$key")
      if [[ -n "$value" ]]; then
        source=$(get_config_source "$key")
        # Mask tokens
        [[ "$key" == *token* ]] && value="***"
        echo "| $key | $value | $source |"
      fi
    done
    echo
    echo "**Config locations:**"
    echo "- System: /etc/basecamp/config.json"
    echo "- User: ~/.config/basecamp/config.json"
    echo "- Repo: <git-root>/.basecamp/config.json"
    echo "- Local: .basecamp/config.json"
  fi
}

_config_init() {
  ensure_local_config_dir
  if [[ -f "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE" ]]; then
    info "Config file already exists: $BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
    return
  fi

  echo '{}' > "$BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
  info "Created: $BCQ_LOCAL_CONFIG_DIR/$BCQ_CONFIG_FILE"
}

_config_set() {
  local key="$1"
  local value="$2"
  local scope="local"

  shift 2 || die "Usage: bcq config set <key> <value> [--global]" $EXIT_USAGE

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --global) scope="global"; shift ;;
      --local) scope="local"; shift ;;
      *) shift ;;
    esac
  done

  if [[ "$scope" == "global" ]]; then
    set_global_config "$key" "$value"
    info "Set $key = $value (global)"
  else
    set_local_config "$key" "$value"
    info "Set $key = $value (local)"
  fi
}

_config_unset() {
  local key="$1"
  local scope="${2:---local}"

  unset_config "$key" "$scope"
  info "Unset $key"
}

_config_project() {
  # Interactive project picker
  local projects
  projects=$(api_get "/projects.json")

  local count
  count=$(echo "$projects" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    die "No projects found" $EXIT_NOT_FOUND
  fi

  echo "Available projects:"
  echo
  echo "$projects" | jq -r 'to_entries[] | "\(.key + 1). \(.value.name) (#\(.value.id))"'
  echo

  read -rp "Select project (1-$count): " selection

  if [[ ! "$selection" =~ ^[0-9]+$ ]] || [[ "$selection" -lt 1 ]] || [[ "$selection" -gt "$count" ]]; then
    die "Invalid selection" $EXIT_USAGE
  fi

  local project_id project_name
  project_id=$(echo "$projects" | jq -r ".[$((selection - 1))].id")
  project_name=$(echo "$projects" | jq -r ".[$((selection - 1))].name")

  set_local_config "project_id" "$project_id"
  info "Set project_id = $project_id ($project_name)"

  # Optionally select default todolist
  read -rp "Select default todolist? (y/N): " select_todolist
  if [[ "$select_todolist" =~ ^[Yy] ]]; then
    _config_todolist "$project_id"
  fi
}


_config_todolist() {
  local project_id="$1"

  # Get todoset from project
  local project_data todoset_id
  project_data=$(api_get "/projects/$project_id.json")
  todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')

  if [[ -z "$todoset_id" ]]; then
    warn "No todoset found in project"
    return
  fi

  local todolists
  todolists=$(api_get "/buckets/$project_id/todosets/$todoset_id/todolists.json")

  local count
  count=$(echo "$todolists" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    warn "No todolists found"
    return
  fi

  echo
  echo "Available todolists:"
  echo
  echo "$todolists" | jq -r 'to_entries[] | "\(.key + 1). \(.value.name) (#\(.value.id))"'
  echo

  read -rp "Select todolist (1-$count): " selection

  if [[ ! "$selection" =~ ^[0-9]+$ ]] || [[ "$selection" -lt 1 ]] || [[ "$selection" -gt "$count" ]]; then
    warn "Invalid selection, skipping todolist"
    return
  fi

  local todolist_id todolist_name
  todolist_id=$(echo "$todolists" | jq -r ".[$((selection - 1))].id")
  todolist_name=$(echo "$todolists" | jq -r ".[$((selection - 1))].name")

  set_local_config "todolist_id" "$todolist_id"
  info "Set todolist_id = $todolist_id ($todolist_name)"
}

cmd_mcp() {
  local action="${1:-}"
  shift || true

  case "$action" in
    serve)
      _mcp_serve "$@"
      ;;
    *)
      die "Unknown mcp action: $action" $EXIT_USAGE "Usage: bcq mcp serve"
      ;;
  esac
}

_mcp_serve() {
  die "MCP server not yet implemented" $EXIT_USAGE
}
