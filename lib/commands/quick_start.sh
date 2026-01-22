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
  local topic=""
  local json_output=false
  local format
  format=$(get_format)

  # Parse arguments
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h) _help_help; return ;;
      --json) json_output=true; shift ;;
      --*) shift ;;  # Skip unknown flags
      *) topic="$1"; shift ;;
    esac
  done

  # Help topic (exit-codes, json-output)
  # Pass json flag to topic handler for topic-specific JSON output
  if [[ -n "$topic" ]]; then
    _help_topic "$topic" "$json_output"
    return
  fi

  # JSON output only when explicitly requested (not auto-detected)
  # Help is fundamentally human-facing; --json must be explicit
  if [[ "$json_output" == "true" ]]; then
    _help_full_json
    return
  fi

  # Default: comprehensive categorized help
  _help_full
}

# Emit SKILL.md — separate command, not a help format
cmd_skill() {
  case "${1:-}" in
    --help|-h) _help_skill_cmd; return ;;
  esac
  _help_emit_skill
}

_help_skill_cmd() {
  cat << 'EOF'
bcq skill - Emit SKILL.md for agent frameworks

USAGE
  bcq skill              Emit SKILL.md to stdout
  bcq skill > SKILL.md   Save to file

The generated skill includes domain invariants from lib/agent_invariants.json.
EOF
}

_help_help() {
  cat << 'EOF'
bcq help - Show help information

USAGE
  bcq help [topic]
  bcq help --json

TOPICS
  exit-codes    Exit code reference
  json-output   JSON output format documentation

OPTIONS
  --json        Machine-readable JSON output

EXAMPLES
  bcq help              Full categorized help
  bcq help exit-codes   Exit code reference
  bcq help --json       JSON output for programmatic use
  bcq skill             Generate SKILL.md (separate command)
EOF
}

# Default help: agent-optimized, token-efficient
_help_default() {
  local invariants_file="$BCQ_ROOT/lib/agent_invariants.json"

  cat << EOF
bcq v$BCQ_VERSION — Basecamp CLI

Use bcq for all Basecamp operations. Never call the API directly.

COMMANDS
  bcq projects                  List projects
  bcq todos --assignee me       My assigned todos
  bcq todo "content"            Create a todo
  bcq done <id>                 Complete a todo
  bcq comment "text" <id>       Add a comment
  bcq search "query"            Search across projects
  bcq <command> --help          Command-specific help

INVARIANTS
EOF

  # Load invariants from JSON
  if [[ -f "$invariants_file" ]]; then
    jq -r '.invariants[] | "  \(.title): \(.description)"' "$invariants_file"
  fi

  cat << 'EOF'

AVOID
  Calling Basecamp API directly — bypasses auth refresh, rate limiting, pagination
  Assuming project has a feature — check dock[] first
  Confusing todoset with todolist — todoset is container, todolist is the list
EOF
}

# Emit SKILL.md format
_help_emit_skill() {
  local invariants_file="$BCQ_ROOT/lib/agent_invariants.json"

  cat << EOF
# GENERATED by bcq $BCQ_VERSION — do not edit
# Regenerate with: bcq skill
---
name: basecamp
version: "$BCQ_VERSION"
description: Basecamp API via bcq CLI
tools:
  - Bash
---

# Basecamp via bcq

Use \`bcq\` for all Basecamp operations. **Always pass \`--json\` for structured output.**

## Critical: Explicit JSON Output

Always pass \`--json\` flag. Do NOT rely on implicit output detection:
\`\`\`bash
bcq todos --project ID --json     # Correct: explicit JSON
bcq todos --project ID            # Avoid: format depends on TTY
\`\`\`

## Domain Invariants

These trip up models — internalize them:

EOF

  # Load invariants from JSON
  if [[ -f "$invariants_file" ]]; then
    jq -r '.invariants[] | "- **\(.title)**: \(.description)"' "$invariants_file"
  fi

  cat << 'EOF'

## Preferred Patterns

```bash
EOF

  if [[ -f "$invariants_file" ]]; then
    jq -r '.preferred_patterns[] | "\(.pattern)"' "$invariants_file"
  fi

  cat << 'EOF'
```

## Anti-Patterns

EOF

  if [[ -f "$invariants_file" ]]; then
    jq -r '.anti_patterns[] | "- **\(.pattern)**: \(.why)"' "$invariants_file"
  fi

  cat << 'EOF'

## Commands Reference

Run `bcq --help` for full command list. Key commands:

| Command | Description |
|---------|-------------|
| `bcq projects --json` | List projects |
| `bcq todos --project ID --json` | List todos in project |
| `bcq todo "content" --project ID` | Create a todo |
| `bcq done <id> --project ID` | Complete a todo |
| `bcq comment "text" --on <id>` | Add a comment |
| `bcq search "query" --json` | Search across projects |
| `bcq show TYPE/ID --json` | Show any recording |

Never call the Basecamp API directly when bcq can do it.
EOF
}

# JSON format
_help_json() {
  local invariants_file="$BCQ_ROOT/lib/agent_invariants.json"

  local invariants='[]'
  local preferred_patterns='[]'
  local anti_patterns='[]'

  if [[ -f "$invariants_file" ]]; then
    invariants=$(jq -c '.invariants' "$invariants_file")
    preferred_patterns=$(jq -c '.preferred_patterns' "$invariants_file")
    anti_patterns=$(jq -c '.anti_patterns' "$invariants_file")
  fi

  jq -nc \
    --arg version "$BCQ_VERSION" \
    --argjson invariants "$invariants" \
    --argjson preferred_patterns "$preferred_patterns" \
    --argjson anti_patterns "$anti_patterns" \
    '{
      name: "basecamp",
      version: $version,
      description: "Basecamp API via bcq CLI",
      tools: ["Bash"],
      invariants: $invariants,
      preferred_patterns: $preferred_patterns,
      anti_patterns: $anti_patterns,
      commands: {
        projects: "List projects",
        todos: "List todos",
        todo: "Create a todo",
        done: "Complete a todo",
        comment: "Add a comment",
        search: "Search across projects",
        show: "Show any recording"
      },
      help_commands: {
        general: "bcq --help",
        specific: "bcq <command> --help",
        skill: "bcq skill"
      }
    }'
}

_help_main() {
  cat << 'EOF'
bcq - Basecamp Query Tool

USAGE
  bcq <command> [options]

COMMANDS
  Query
    campfire        List campfires, view/post messages
    cards           List cards (kanban)
    files           List files, folders, documents
    me              Show current user
    messages        List messages
    people          List people
    projects        List projects
    recordings      Browse recordings by type
    search          Search across projects
    show            Show details of a resource
    todolists       List todolists
    todos           List todos
    webhooks        Manage webhooks

  Actions
    assign          Assign a todo
    comment         Add a comment
    done            Complete a todo
    message         Post a message
    reopen          Reopen a completed todo
    todo            Create a todo

  Config
    auth            Authentication (login, logout, status)
    config          Configuration management

  MCP
    mcp serve       Start MCP server

  Meta
    version         Show version
    self-update     Update (installer installs only)

GLOBAL FLAGS
  --json, -j        Force JSON output
  --md, -m          Force Markdown output
  --quiet, -q       Minimal output
  --verbose, -v     Debug output (shows curl commands)
  --project, -p     Override project context
  --account, -a     Override account context
  --cache-dir       Override cache directory

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
bcq todos - List and manage todos

USAGE
  bcq todos [action] [options]

ACTIONS
  list              List todos (default)
  show <id>         Show todo details
  create "content"  Create a todo
  complete <id>     Mark todo as done
  uncomplete <id>   Reopen a completed todo
  position <id>     Change todo position

OPTIONS
  --in <project>    Filter by project (ID or name)
  --list <list>     Filter by todolist (ID or name)
  --assignee <who>  Filter by assignee (me or numeric ID)
  --status <s>      Filter by status (active, completed)
  --overdue         Show only overdue todos (past due, not completed)
  --to <position>   Target position for reorder (1 = top)
  --json, -j        JSON output

EXAMPLES
  bcq todos                        Your assigned todos
  bcq todos --in "Basecamp 4"      Todos in a project
  bcq todos --status completed     Completed todos
  bcq reopen 123                   Reopen completed todo
  bcq todos position 123 --to 1    Move todo to top

NOTES
  Todos use a boolean 'completed' field, not a status string. Filter by status
  using --status active or --status completed. When assigning, use assignee_ids
  as an array (even for single assignment).
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
  refresh           Force token refresh

OPTIONS (for login)
  --scope <scope>   Request 'full' (read+write) or 'read' (read-only) access
                    Default: full. Use 'read' for least-privilege access.
                    Note: Launchpad OAuth always grants full access (see below).
  --no-browser      Manual authorization code entry (headless mode)

OAUTH PROVIDERS
  bcq supports two OAuth providers:

  1. Basecamp OAuth 2.1 (BC3) - Auto-discovered via .well-known endpoint
     - Uses Dynamic Client Registration (DCR)
     - Uses PKCE for security
     - Supports --scope read for least-privilege access
     - No configuration required

  2. Launchpad OAuth 2 - Fallback when BC3 OAuth is unavailable
     - Requires pre-registered client credentials
     - Register at https://integrate.37signals.com
     - Set credentials via environment variables or config
     - Always grants full access (--scope read is ignored with a warning)

ENVIRONMENT VARIABLES
  BCQ_CLIENT_ID       Launchpad OAuth client ID
  BCQ_CLIENT_SECRET   Launchpad OAuth client secret

  Alternatively, set in config:
    bcq config set --global oauth_client_id <id>
    bcq config set --global oauth_client_secret <secret>

EXAMPLES
  bcq auth login                   Full access (default)
  bcq auth login --scope read      Read-only access
  bcq auth status
  BCQ_CLIENT_ID=xxx BCQ_CLIENT_SECRET=yyy bcq auth login
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

_help_people() {
  cat << 'EOF'
bcq people - List and show people

USAGE
  bcq people [subcommand] [options]

SUBCOMMANDS
  list              List all people (default)
  show <id>         Show person details
  pingable          List people who can receive pings

OPTIONS
  --project, -p     Limit to people on a specific project

EXAMPLES
  bcq people                    List all people
  bcq people --project 123      People on project
  bcq people show 456           Show person details
  bcq people pingable           Pingable people
EOF
}

_help_recordings() {
  cat << 'EOF'
bcq recordings - List and manage recordings across projects

USAGE
  bcq recordings [type] [options]
  bcq recordings trash <id> --in <project>         Trash a recording
  bcq recordings archive <id> --in <project>       Archive a recording
  bcq recordings restore <id> --in <project>       Restore a recording
  bcq recordings visibility <id> --visible|--hidden --in <project>

TYPES (for listing)
  todos             Todo items
  messages          Message board posts
  documents         Documents
  comments          Comments
  cards             Kanban cards
  uploads           Uploaded files

OPTIONS
  --type, -t        Recording type (or use shorthand above)
  --project, -p     Filter by project ID(s)
  --status, -s      active (default), archived, or trashed
  --sort            created_at or updated_at (default)
  --direction       desc (default) or asc
  --limit, -n       Limit results

CLIENT VISIBILITY (modern clients setup)
  --visible         Make recording visible to clients
  --hidden          Hide recording from clients

EXAMPLES
  bcq recordings todos                     Recent todos across all projects
  bcq recordings messages --project 123    Messages in specific project
  bcq recordings comments --limit 20       Last 20 comments
  bcq recordings visibility 456 --visible --in 123  Show to clients
  bcq recordings visibility 456 --hidden --in 123   Hide from clients

NOTE: Client visibility controls what clients (project participants) can see.
Not all recordings support visibility toggling - some inherit from parent.
EOF
}
