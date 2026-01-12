#!/usr/bin/env bash
# stubs.sh - Placeholder commands (not yet implemented)


cmd_todolists() {
  die "Command 'todolists' not yet implemented" $EXIT_USAGE
}


cmd_show() {
  die "Command 'show' not yet implemented" $EXIT_USAGE
}


cmd_assign() {
  die "Command 'assign' not yet implemented" $EXIT_USAGE
}

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

  if [[ "$format" == "json" ]]; then
    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "set" "bcq config set <key> <value>" "Set config value")" \
      "$(breadcrumb "project" "bcq config project" "Select project")"
    )
    json_ok "$config" "Effective configuration" "$bcs"
  else
    echo "## Configuration"
    echo
    echo '```json'
    echo "$config" | jq .
    echo '```'
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
  # Interactive project picker - requires API
  die "Interactive project picker not yet implemented" $EXIT_USAGE \
    "Use: bcq config set project_id <id>"
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
