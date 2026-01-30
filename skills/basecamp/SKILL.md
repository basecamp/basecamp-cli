---
name: basecamp
description: |
  Interact with Basecamp via the bcq CLI. Search projects, list todos, create tasks,
  post to campfire, manage cards, and link code to Basecamp items. Use this skill
  for ANY Basecamp-related question or action.
triggers:
  # Direct invocations
  - basecamp
  - /basecamp
  - bcq
  # Actions
  - basecamp todo
  - basecamp project
  - basecamp card
  - basecamp campfire
  - link to basecamp
  - track in basecamp
  - post to basecamp
  - comment on basecamp
  - complete todo
  - mark done
  # Search and discovery
  - search basecamp
  - find in basecamp
  - look up basecamp
  - check basecamp
  - list basecamp
  - show basecamp
  - get from basecamp
  - fetch from basecamp
  # Questions and capability discovery
  - can I basecamp
  - can we basecamp
  - how do I basecamp
  - what's in basecamp
  - what basecamp
  - does basecamp
  - is there a basecamp
  # My work
  - my todos
  - my tasks
  - my basecamp
  - assigned to me
  # URLs
  - 3.basecamp.com
  - basecampapi.com
  - https://3.basecamp.com/
invocable: true
argument-hint: "[action] [args...]"
---

# /basecamp - Basecamp Workflow Command

Interact with Basecamp: create todos, check project status, link code to tasks.

**Always pass `--json` for structured output.**

## URL Parsing

Parse a Basecamp URL to extract its components:

```bash
bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598" --json
```

Returns: `account_id`, `bucket_id`, `type`, `recording_id`, `comment_id` (from fragment).

**URL Structure:**
```
https://3.basecamp.com/{account_id}/buckets/{project_id}/{type}/{id}#__recording_{comment_id}
```

**URL Pattern Examples:**
- `/buckets/27/messages/123` → Message 123 in project 27
- `/buckets/27/messages/123#__recording_456` → Comment 456 on message 123
- `/buckets/27/card_tables/cards/789` → Card 789 in project 27
- `/buckets/27/card_tables/columns/456` → Column 456 in project 27 (for creating cards)
- `/buckets/27/todos/101` → Todo 101 in project 27

**Fetch with bcq:**
```bash
# Show any recording by ID
bcq show <type> <id> --project <project_id> --json

# Example: fetch a message
bcq show message 9478142982 --project 41746046 --json
```

**Replying to Comments:**

Comments are flat on the parent recording (no nested replies). To reply:
```bash
# Parse the URL to get IDs
bcq url parse "https://3.basecamp.com/.../messages/123#__recording_456" --json

# Comment on the parent recording (message 123), not the comment
bcq comment --content "Your reply" --on 123 --project <project_id>
```

## Context

Check current project context:
```bash
cat .basecamp/config.json 2>/dev/null || echo "No project configured"
```

Check git context:
```bash
git branch --show-current 2>/dev/null || echo "Not a git repo"
```

## Available Actions

### List & Navigate

```bash
bcq projects --json                      # List all projects
bcq todos --in <project_id> --json       # List todos in project
bcq todos --assignee me --json           # My todos (requires project)
bcq people --json                        # List people
bcq people --project <id> --json         # People on a project
```

### Create & Modify

```bash
bcq todo --content "Task" --in <project_id>              # Create todo
bcq todo --content "Task" --in <id> --due tomorrow       # With due date
bcq todo --content "Task" --in <id> --assignee me        # Assign to self
bcq done <todo_id> --project <project_id>                # Complete todo
bcq comment --content "Text" --on <recording_id> --project <id>  # Add comment
```

### Cards & Campfire

```bash
bcq cards --in <project_id> --json                       # List all cards
bcq cards columns --in <project_id> --json               # List columns with IDs
bcq cards --in <project_id> --column <column_id> --json  # List cards in column
bcq card --title "Title" --in <project_id>               # Create card (first column)
bcq card --title "Title" --in <project_id> --column <id> # Create card in column
bcq card --title "Title" --content "<p>Body</p>" --in <project_id>  # With HTML body
bcq cards move <card_id> --to "Done" --project <project_id>  # Move card
bcq campfire post --content "Message" --in <project_id>  # Post to campfire
```

**Creating cards from a column URL:**
```bash
# Parse the URL to get project and column IDs
bcq url parse "https://3.basecamp.com/.../card_tables/columns/456" --json
# Returns breadcrumbs with exact command to use

# Create card in that column (numeric column ID skips card table discovery)
bcq card --title "Title" --in <project_id> --column <column_id> --json
```

**Multi-card-table projects:** When a project has multiple card tables, use the numeric column ID from the URL. This bypasses card table discovery and works directly.

## Common Workflows

### Link Code to Basecamp

When working on code related to a Basecamp todo:

```bash
# Link a commit
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
bcq comment --content "Commit $COMMIT: $MSG" --on <todo_id> --project <project_id>

# Link a PR
bcq comment --content "PR: https://github.com/org/repo/pull/42" --on <todo_id> --project <project_id>

# Complete when done
bcq done <todo_id> --project <project_id>
```

### Track Work Progress

```bash
# Create todo for current work
bcq todo --content "Implement feature X" --in <project_id> --assignee me

# Post status update to campfire
bcq campfire post --content "Starting work on feature X" --in <project_id>

# When done
bcq done <todo_id> --project <project_id>
bcq campfire post --content "Completed feature X" --in <project_id>
```

### Set Up Project Context

For a repo linked to a Basecamp project:

```bash
# Create local config
bcq config init
bcq config set project_id <project_id>
bcq config set todolist_id <todolist_id>  # Optional default

# Now commands use defaults
bcq todos --json                  # No --in needed
bcq todo --content "Task"         # Creates in default project/list
```

## Smart Defaults

- `--assignee me` - Resolves to current user's ID
- `--due tomorrow` - Natural date parsing (today, tomorrow, +3, next week)
- Project from `.basecamp/config.json` if not specified

## Output

Always pass `--json` for predictable structured output:

```bash
bcq todos --in 123 --json       # JSON envelope (recommended)
bcq todos --in 123 -q           # Raw JSON (no envelope)
```

## Error Handling

**Connection refused / Network errors:**
```bash
# Check if localhost URLs are configured (from local dev testing)
cat ~/.config/basecamp/config.json
# If base_url or api_url point to localhost, remove them:
# The file should only contain {"account_id": "<your_id>"}
```

**Not found errors:**
```bash
# Verify auth is working
bcq auth status
# Check which accounts you have access to
cat ~/.config/basecamp/accounts.json | jq '."https://3.basecampapi.com"'
# Update config.json with correct account_id
```

**Permission denied: read-only token:**
```bash
bcq auth login --scope full    # Re-auth with write access
```

**Invalid flag errors:**
All shortcut commands require explicit flags:
- `bcq todo --content "text"` (not `bcq todo "text"`)
- `bcq card --title "title"` (not `bcq card "title"`)

**Network error (curl exit 3) - URL malformed:**
This usually means special characters in content broke the URL. Try:
```bash
# Test with simple content first
bcq card --title "Test" --in <project_id> --json

# Avoid special characters in --content; use plain text or escaped HTML
# Bad:  --content "<p>Line1\nLine2</p>"
# Good: --content "<p>Line1</p><p>Line2</p>"
```

## Learn More

For API domain model (buckets, recordings, dock, etc.):
https://github.com/basecamp/bc3-api#key-concepts
