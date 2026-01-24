#!/usr/bin/env bash
# help.sh - Comprehensive help system

# Command metadata is now stored in lib/commands/commands.json
# This provides a single source of truth for command discovery

# Path to commands metadata (relative to script directory)
_BCQ_COMMANDS_JSON=""

_get_commands_json_path() {
  if [[ -z "$_BCQ_COMMANDS_JSON" ]]; then
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    _BCQ_COMMANDS_JSON="$script_dir/commands/commands.json"
  fi
  echo "$_BCQ_COMMANDS_JSON"
}

_load_commands_metadata() {
  local json_file
  json_file=$(_get_commands_json_path)
  if [[ -f "$json_file" ]]; then
    cat "$json_file"
  else
    echo '{"categories": [], "commands": []}'
  fi
}

# Help topics
BCQ_HELP_TOPICS=(
  "exit-codes:Exit code reference"
  "json-output:JSON output format documentation"
)

# Main help output
_help_full() {
  cat << 'EOF'
bcq — Basecamp CLI

USAGE
  bcq <command> [action] [flags]

CORE COMMANDS
  projects      Manage projects (list, show, create, update, delete)
  todos         Manage to-dos (list, show, create, complete, uncomplete)
  todolists     Manage to-do lists
  messages      Manage messages (list, show, create, pin, unpin)
  campfire      Chat in Campfire rooms
  cards         Manage Kanban cards and columns

SHORTCUT COMMANDS
  todo          Create a to-do (→ todos create)
  done          Complete a to-do (→ todos complete)
  reopen        Uncomplete a to-do (→ todos uncomplete)
  message       Post a message (→ messages create)
  card          Create a card (→ cards create)
  comment       Add a comment
  assign        Assign a recording
  unassign      Remove assignment

FILES & DOCS
  files         Manage files, documents, and folders
  uploads       List and manage uploads
  vaults        Manage folders (vaults)
  docs          Manage documents

SCHEDULING & TIME
  schedule      Manage schedule entries
  timesheet     View time tracking reports
  checkins      View automatic check-ins (questionnaires)

ORGANIZATION
  people        Manage people and access
  templates     Manage project templates
  webhooks      Manage webhooks
  lineup        Manage lineup markers

COMMUNICATION
  messageboards View message boards
  messagetypes  Manage message categories
  forwards      Manage email forwards (inbox)
  subscriptions Manage notification subscriptions

SEARCH & BROWSE
  search        Search across projects
  recordings    Browse recordings by type/status
  show          Show any recording by ID
  events        View recording change history

AUTH & CONFIG
  auth          Authenticate with Basecamp
  config        Manage configuration
  me            Show current user profile

ADDITIONAL COMMANDS
  commands      List all commands (for discovery)
  mcp           MCP server integration
  skill         Generate SKILL.md for agent frameworks

FLAGS
  --help, -h    Show help for command
  --json        Output as JSON
  --version     Show version

EXAMPLES
  bcq projects                     List all projects
  bcq todos --assignee me          My assigned to-dos
  bcq todo "Ship feature" -l 123   Create a to-do
  bcq done 456                     Complete to-do #456
  bcq search "budget"              Search for "budget"

LEARN MORE
  bcq <command> --help      Command-specific help
  bcq help exit-codes       Exit code reference

API DOCUMENTATION
  https://github.com/basecamp/bc3-api#key-concepts
EOF
}

# JSON output for programmatic discovery
_help_full_json() {
  local metadata
  metadata=$(_load_commands_metadata)

  # Return structured data from JSON file with version added
  echo "$metadata" | jq --arg version "$BCQ_VERSION" '. + {version: $version}'
}

# Help topic: exit codes
_help_topic_exit_codes() {
  cat << 'EOF'
# bcq Exit Codes

Exit codes follow standard conventions for scripting and error handling.

## Exit Codes

| Code | Name | Description |
|------|------|-------------|
| 0 | OK | Success |
| 1 | USAGE | Invalid usage, bad arguments |
| 2 | NOT_FOUND | Resource not found |
| 3 | AUTH | Authentication required or failed |
| 4 | FORBIDDEN | Permission denied |
| 5 | RATE_LIMIT | Rate limit exceeded (retry after delay) |
| 6 | NETWORK | Network error (connection failed) |
| 7 | API | API error (server-side issue) |
| 8 | AMBIGUOUS | Ambiguous input (multiple matches) |

## Usage in Scripts

```bash
bcq todos show 123
case $? in
  0) echo "Success" ;;
  2) echo "Todo not found" ;;
  3) echo "Need to login: bcq auth login" ;;
  *) echo "Error occurred" ;;
esac
```

## JSON Error Output

When using --json, errors include structured information:

```json
{
  "ok": false,
  "error": "Todo not found",
  "code": "not_found",
  "hint": "Check the todo ID or project context"
}
```
EOF
}

# Help topic: json output
_help_topic_json_output() {
  cat << 'EOF'
# bcq JSON Output

All bcq commands support `--json` for machine-readable output.

## Success Response

```json
{
  "ok": true,
  "data": { ... },
  "summary": "3 todos",
  "breadcrumbs": [
    {"action": "show", "cmd": "bcq todos show 123", "description": "View details"}
  ]
}
```

## Error Response

```json
{
  "ok": false,
  "error": "Resource not found",
  "code": "not_found",
  "hint": "Check the ID or project context"
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| ok | boolean | Success indicator |
| data | object/array | Response payload |
| summary | string | Human-readable summary |
| breadcrumbs | array | Suggested next actions |
| error | string | Error message (if ok=false) |
| code | string | Error code (if ok=false) |
| hint | string | Recovery suggestion (if ok=false) |

## Parsing with jq

```bash
# Get raw data
bcq projects --json | jq '.data'

# Extract specific field
bcq todos show 123 --json | jq '.data.content'

# Check success
if bcq todos show 123 --json | jq -e '.ok' > /dev/null; then
  echo "Success"
fi
```
EOF
}

# Dispatch help topic
# Usage: _help_topic <topic> [--json]
_help_topic() {
  local topic="$1"
  local json_output="${2:-false}"

  case "$topic" in
    exit-codes|exit_codes|exitcodes)
      _help_topic_exit_codes
      ;;
    json-output|json_output|json)
      _help_topic_json_output
      ;;
    *)
      echo "Unknown help topic: $topic"
      echo
      echo "Available topics:"
      for entry in "${BCQ_HELP_TOPICS[@]}"; do
        IFS=':' read -r name description <<< "$entry"
        printf "  %-14s %s\n" "$name" "$description"
      done
      return 1
      ;;
  esac
}

# Commands command for programmatic discovery
cmd_commands() {
  local format json_output=false
  format=$(get_format)

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --json) json_output=true; shift ;;
      --help|-h) _help_commands_cmd; return ;;
      *) shift ;;
    esac
  done

  if [[ "$json_output" == "true" ]] || [[ "$format" == "json" ]]; then
    _help_full_json
  else
    _commands_list_md
  fi
}

_commands_list_md() {
  local metadata
  metadata=$(_load_commands_metadata)

  echo "# bcq Commands"
  echo

  # Iterate over categories from JSON
  local categories
  categories=$(echo "$metadata" | jq -r '.categories[] | "\(.id):\(.label)"')

  while IFS=: read -r cat_id cat_label; do
    [[ -z "$cat_id" ]] && continue

    echo "## $cat_label"
    echo

    # Get commands for this category and format with printf for proper padding
    echo "$metadata" | jq -r --arg cat "$cat_id" \
      '.commands[] | select(.category == $cat) | "\(.name)\t\(.description)"' | \
      while IFS=$'\t' read -r name desc; do
        printf "  %-14s %s\n" "$name" "$desc"
      done

    echo
  done <<< "$categories"
}

_help_commands_cmd() {
  cat << 'EOF'
bcq commands - List all available commands

USAGE
  bcq commands [--json]

OPTIONS
  --json    Machine-readable JSON output

OUTPUT
  Lists all bcq commands grouped by category, with descriptions
  and available actions for each command.

EXAMPLES
  bcq commands              List all commands
  bcq commands --json       JSON output for programmatic use
EOF
}
