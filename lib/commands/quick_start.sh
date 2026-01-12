#!/usr/bin/env bash
# quick_start.sh - No-args handler for bcq


cmd_quick_start() {
  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    _quick_start_json
  else
    _quick_start_md
  fi
}

_quick_start_json() {
  local auth_status="unauthenticated"
  local auth_user=""
  local account_name=""
  local project_name=""
  local stats='{}'

  if get_access_token &>/dev/null; then
    auth_status="authenticated"
    # Try to get user info from cached accounts
    local accounts
    accounts=$(load_accounts)
    if [[ "$accounts" != "[]" ]]; then
      local account_id
      account_id=$(get_account_id)
      if [[ -n "$account_id" ]]; then
        account_name=$(echo "$accounts" | jq -r --arg id "$account_id" '.[] | select(.id == ($id | tonumber)) | .name // empty')
      fi
    fi
  fi

  local project_id
  project_id=$(get_project_id)
  if [[ -n "$project_id" ]]; then
    project_name=$(get_config "project_name" "$project_id")
  fi

  jq -n \
    --arg version "$BCQ_VERSION" \
    --arg auth_status "$auth_status" \
    --arg auth_user "$auth_user" \
    --arg account_name "$account_name" \
    --arg project_id "$project_id" \
    --arg project_name "$project_name" \
    '{
      version: $version,
      auth: {
        status: $auth_status,
        user: $auth_user,
        account: $account_name
      },
      context: {
        project_id: (if $project_id != "" then ($project_id | tonumber) else null end),
        project_name: (if $project_name != "" then $project_name else null end)
      },
      commands: {
        quick_start: ["bcq projects", "bcq todos", "bcq search \"query\""],
        common: ["bcq todo \"content\"", "bcq done <id>", "bcq comment \"text\" <id>"]
      },
      breadcrumbs: [
        {action: "list_projects", cmd: "bcq projects"},
        {action: "list_todos", cmd: "bcq todos"},
        {action: "authenticate", cmd: "bcq auth login"}
      ]
    }'
}

_quick_start_md() {
  echo "bcq v$BCQ_VERSION — Basecamp Query"

  # Auth status
  if get_access_token &>/dev/null; then
    local accounts account_id account_name
    accounts=$(load_accounts)
    account_id=$(get_account_id)
    if [[ -n "$account_id" ]] && [[ "$accounts" != "[]" ]]; then
      account_name=$(echo "$accounts" | jq -r --arg id "$account_id" '.[] | select(.id == ($id | tonumber)) | .name // empty')
      echo "Auth: ✓ logged in @ $account_name"
    else
      echo "Auth: ✓ logged in"
    fi
  else
    echo "Auth: ✗ not logged in"
  fi
  echo

  # Quick start
  echo "QUICK START"
  echo "  bcq projects              List projects"
  echo "  bcq todos                 Your assigned todos"
  echo "  bcq search \"query\"        Find anything"
  echo

  # Common tasks
  echo "COMMON TASKS"
  echo "  bcq todo \"content\"        Create todo"
  echo "  bcq done <id>             Complete todo"
  echo "  bcq comment \"text\" <id>   Add comment"
  echo

  # Context
  local project_id project_name
  project_id=$(get_project_id)
  if [[ -n "$project_id" ]]; then
    project_name=$(get_config "project_name" "$project_id")
    echo "CONTEXT"
    echo "  Project: ${project_name:-$project_id}"
    echo
  fi

  # Next action
  if ! get_access_token &>/dev/null; then
    echo "NEXT: bcq auth login"
  else
    echo "NEXT: bcq todos"
  fi
}


cmd_help() {
  local command="${1:-}"

  if [[ -z "$command" ]]; then
    _help_main
  else
    case "$command" in
      projects) _help_projects ;;
      todos) _help_todos ;;
      auth) _help_auth ;;
      config) _help_config ;;
      *) _help_main ;;
    esac
  fi
}

_help_main() {
  cat << 'EOF'
bcq - Basecamp Query CLI

USAGE
  bcq <command> [options]

COMMANDS
  Query
    projects        List projects
    todos           List todos
    todolists       List todolists
    search          Search across projects
    show            Show details of a resource
    people          List people
    me              Show current user

  Actions
    todo            Create a todo
    done            Complete a todo
    comment         Add a comment
    assign          Assign a todo

  Config
    auth            Authentication (login, logout, status)
    config          Configuration management

  MCP
    mcp serve       Start MCP server

GLOBAL FLAGS
  --json, -j        Force JSON output
  --md, -m          Force Markdown output
  --quiet, -q       Minimal output
  --verbose, -v     Debug output
  --project, -p     Override project context
  --account, -a     Override account context

EXAMPLES
  bcq projects                    List all projects
  bcq todos --in "Project Name"   List todos in a project
  bcq todo "Fix the bug"          Create a todo
  bcq done 123                    Complete todo #123

Run 'bcq <command> --help' for command-specific help.
EOF
}

_help_projects() {
  cat << 'EOF'
bcq projects - List projects

USAGE
  bcq projects [options]

OPTIONS
  --json, -j        JSON output
  --status <s>      Filter by status (active, archived, trashed)

EXAMPLES
  bcq projects
  bcq projects --json | jq '.[0]'
EOF
}

_help_todos() {
  cat << 'EOF'
bcq todos - List todos

USAGE
  bcq todos [options]

OPTIONS
  --in <project>    Filter by project (ID or name)
  --list <list>     Filter by todolist (ID or name)
  --assignee <who>  Filter by assignee (me, ID, or @handle)
  --status <s>      Filter by status (active, completed)
  --json, -j        JSON output

EXAMPLES
  bcq todos                        Your assigned todos
  bcq todos --in "Basecamp 4"      Todos in a project
  bcq todos --status completed     Completed todos
EOF
}

_help_auth() {
  cat << 'EOF'
bcq auth - Authentication management

USAGE
  bcq auth <subcommand>

SUBCOMMANDS
  login             Authenticate via OAuth
  logout            Clear stored credentials
  status            Show authentication status

OPTIONS (for login)
  --scope <scope>   Request 'full' (read+write) or 'read' (read-only) access
                    Default: full. Use 'read' for least-privilege access.
  --no-browser      Manual authorization code entry (headless mode)

EXAMPLES
  bcq auth login                   Full access (default)
  bcq auth login --scope read      Read-only access
  bcq auth status
EOF
}

_help_config() {
  cat << 'EOF'
bcq config - Configuration management

USAGE
  bcq config [subcommand] [args]

SUBCOMMANDS
  (none)            Show effective config
  init              Create .basecamp/config.json interactively
  set <key> <val>   Set a config value
  unset <key>       Remove a config value
  project           Select default project interactively

OPTIONS
  --global          Apply to global config
  --local           Apply to local config (default)

CONFIG KEYS
  account_id        Default Basecamp account ID
  project_id        Default project ID
  project_name      Project display name
  todolist_id       Default todolist ID

EXAMPLES
  bcq config                       Show all config
  bcq config set project_id 123    Set default project
  bcq config project               Interactive project picker
EOF
}
