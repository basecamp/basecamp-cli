#!/usr/bin/env bash
# post-commit-check.sh - Check for Basecamp todo references after git commits
#
# This hook runs after Bash tool use and checks if a git commit was made
# that references a Basecamp todo (BC-12345, todo-12345, etc.)

set -euo pipefail

# Read tool input from stdin (JSON with tool_name, tool_input, tool_output)
input=$(cat)

# Extract tool input (the bash command that was run)
tool_input=$(echo "$input" | jq -r '.tool_input.command // empty' 2>/dev/null)

# Only process git commit commands
if [[ ! "$tool_input" =~ ^git\ commit ]]; then
  exit 0
fi

# Check if commit succeeded by looking for output patterns
tool_output=$(echo "$input" | jq -r '.tool_output // empty' 2>/dev/null)

# Verify commit actually succeeded - look for commit hash pattern or "create mode"
if [[ ! "$tool_output" =~ \[.*[a-f0-9]{7,}\] ]] && [[ ! "$tool_output" =~ "create mode" ]]; then
  # Commit likely failed, don't suggest linking
  exit 0
fi

# Look for todo references in the commit message or branch name
branch=$(git branch --show-current 2>/dev/null || true)
last_commit_msg=$(git log -1 --format=%s 2>/dev/null || true)

# Patterns: BC-12345, todo-12345, basecamp-12345, #12345
todo_patterns='BC-[0-9]+|todo-[0-9]+|basecamp-[0-9]+|#[0-9]{5,}'

found_in_branch=$(echo "$branch" | grep -oEi "$todo_patterns" | head -1 || true)
found_in_msg=$(echo "$last_commit_msg" | grep -oEi "$todo_patterns" | head -1 || true)

if [[ -n "$found_in_branch" ]] || [[ -n "$found_in_msg" ]]; then
  ref="${found_in_msg:-$found_in_branch}"
  # Extract just the number
  todo_id=$(echo "$ref" | grep -oE '[0-9]+')

  cat << EOF
<hook-output>
Detected Basecamp todo reference: $ref

To link this commit to Basecamp:
  bcq comment "Commit $(git rev-parse --short HEAD 2>/dev/null): $last_commit_msg" --on $todo_id

Or complete the todo:
  bcq done $todo_id
</hook-output>
EOF
fi
