---
name: basecamp
description: |
  Primary Basecamp workflow command. Create todos, check status, link code to tasks,
  and coordinate with your team. Works with bcq.
triggers:
  - basecamp
  - /basecamp
  - basecamp todo
  - basecamp project
  - link to basecamp
  - track in basecamp
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

When given a Basecamp URL, parse it to understand what's being referenced:

```
https://3.basecamp.com/{account_id}/buckets/{project_id}/{type}/{id}#__recording_{comment_id}
```

**URL Pattern Examples:**
- `/buckets/27/messages/123` → Message 123 in project 27
- `/buckets/27/messages/123#__recording_456` → Comment 456 on message 123
- `/buckets/27/card_tables/cards/789` → Card 789 in project 27
- `/buckets/27/todos/101` → Todo 101 in project 27
- `/buckets/27/todolists/202` → Todolist 202 in project 27

**Fetch with bcq:**
```bash
# Show any recording by ID (works for messages, todos, cards, comments, etc.)
bcq show <type> <id> --project <project_id> --json

# Example: fetch a card
bcq show card 9486682178 --project 27 --json

# Example: fetch a comment (comments are also recordings)
bcq show comment 9500689518 --project 27 --json
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
bcq cards --in <project_id> --json                       # List cards
bcq card --title "Title" --in <project_id>               # Create card
bcq cards move <card_id> --to "Done" --in <project_id>   # Move card
bcq campfire post --content "Message" --in <project_id>  # Post to campfire
```

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
- `bcq project --name "name"` (not `bcq project "name"`)

## Learn More

For API domain model (buckets, recordings, dock, etc.):
https://github.com/basecamp/bc3-api#key-concepts
