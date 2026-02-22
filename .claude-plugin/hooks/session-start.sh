#!/usr/bin/env bash
# session-start.sh - Load Basecamp context at session start
#
# This hook runs when Claude Code starts a session and outputs
# relevant Basecamp project context if configured.

set -euo pipefail

# Find basecamp - prefer PATH, fall back to plugin's bin directory
if command -v basecamp &>/dev/null; then
  BASECAMP_BIN="basecamp"
else
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  BASECAMP_BIN="${SCRIPT_DIR}/../../bin/basecamp"
  if [[ ! -x "$BASECAMP_BIN" ]]; then
    cat << 'EOF'
<hook-output>
Basecamp plugin: basecamp CLI not found.
Install: https://github.com/basecamp/basecamp-cli#installation
</hook-output>
EOF
    exit 0
  fi
fi

# Check if we have any Basecamp configuration
config_output=$("$BASECAMP_BIN" config show --json 2>/dev/null || echo '{}')
has_config=$(echo "$config_output" | jq -r '.data // empty' 2>/dev/null)

if [[ -z "$has_config" ]] || [[ "$has_config" == "{}" ]]; then
  exit 0
fi

# Extract config values
account_id=$(echo "$has_config" | jq -r '.account_id.value // empty')
project_id=$(echo "$has_config" | jq -r '.project_id.value // empty')
todolist_id=$(echo "$has_config" | jq -r '.todolist_id.value // empty')

# Only output if we have at least account_id
if [[ -z "$account_id" ]]; then
  exit 0
fi

# Build context message
context="Basecamp context loaded:"
context+="\n  Account: $account_id"

if [[ -n "$project_id" ]]; then
  context+="\n  Project: $project_id"
fi

if [[ -n "$todolist_id" ]]; then
  context+="\n  Todolist: $todolist_id"
fi

# Check if authenticated
auth_status=$("$BASECAMP_BIN" auth status --json 2>/dev/null || echo '{}')
is_auth=$(echo "$auth_status" | jq -r '.data.authenticated // false')

if [[ "$is_auth" != "true" ]]; then
  context+="\n  Auth: Not authenticated (run: basecamp auth login)"
fi

cat << EOF
<hook-output>
$(echo -e "$context")

Use \`basecamp\` commands to interact with Basecamp:
  basecamp todos list          # List todos in current project
  basecamp search "query"      # Search across projects
  basecamp comment "msg" --on ID  # Comment on a recording
</hook-output>
EOF
