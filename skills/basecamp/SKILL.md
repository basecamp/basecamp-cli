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
invocable: true
argument-hint: "[action] [args...]"
---

# /basecamp - Basecamp Workflow Command

Interact with Basecamp: create todos, check project status, link code to tasks.

**Always pass `--json` for structured output.**

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
bcq todo "Content" --in <project_id>              # Create todo
bcq todo "Content" --in <id> --due tomorrow       # With due date
bcq todo "Content" --in <id> --assignee me        # Assign to self
bcq done <todo_id>                                # Complete todo
bcq comment "Text" --on <todo_id>                 # Add comment
```

### Cards & Campfire

```bash
bcq cards --in <project_id> --json                # List cards
bcq card "Title" --in <project_id>                # Create card
bcq cards move <card_id> --to "Done"              # Move card
bcq campfire post "Message" --in <project_id>    # Post to campfire
```

## Common Workflows

### Link Code to Basecamp

When working on code related to a Basecamp todo:

```bash
# Link a commit
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
bcq comment "Commit $COMMIT: $MSG" --on <todo_id>

# Link a PR
bcq comment "PR: https://github.com/org/repo/pull/42" --on <todo_id>

# Complete when done
bcq done <todo_id>
```

### Track Work Progress

```bash
# Create todo for current work
bcq todo "Implement feature X" --in <project_id> --assignee me

# Post status update to campfire
bcq campfire post "Starting work on feature X" --in <project_id>

# When done
bcq done <todo_id>
bcq campfire post "Completed feature X" --in <project_id>
```

### Set Up Project Context

For a repo linked to a Basecamp project:

```bash
# Create local config
bcq config init
bcq config set project_id <project_id>
bcq config set todolist_id <todolist_id>  # Optional default

# Now commands use defaults
bcq todos --json         # No --in needed
bcq todo "Task"          # Creates in default project/list
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

If you see "Permission denied: read-only token":
```bash
bcq auth login --scope full    # Re-auth with write access
```

## Learn More

For API domain model (buckets, recordings, dock, etc.):
https://github.com/basecamp/bc3-api#key-concepts
