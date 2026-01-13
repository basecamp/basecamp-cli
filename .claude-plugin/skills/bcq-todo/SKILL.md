---
name: todo
description: |
  Quick todo operations: create, complete, list todos.
  Fast shorthand for common bcq todo workflows.
triggers:
  - /todo
  - quick todo
  - create todo
  - new todo
invocable: true
argument-hint: "<action> [content/id]"
---

# /todo - Quick Todo Operations

Fast todo management with bcq CLI.

## Quick Patterns

Parse the user's request and execute the appropriate command:

### Create Todo
```bash
# "/todo Fix the login bug"
bcq todo "Fix the login bug"

# "/todo Fix bug --due tomorrow"
bcq todo "Fix bug" --due tomorrow

# "/todo Assign to me: review PR"
bcq todo "Review PR" --assignee me
```

### Complete Todo
```bash
# "/todo done 12345"
bcq done 12345

# "/todo complete 12345 12346"
bcq done 12345 12346
```

### List Todos
```bash
# "/todo list"
bcq todos

# "/todo mine"
bcq todos --assignee me
```

## Project Context

Check for configured project:
```bash
bcq config show --json | jq -r '.project_id // empty'
```

If no project configured and user doesn't specify one, list available projects:
```bash
bcq projects
```

Then ask user to specify which project.

## Examples

| Input | Command |
|-------|---------|
| `/todo Fix auth bug` | `bcq todo "Fix auth bug"` |
| `/todo done 123` | `bcq done 123` |
| `/todo list` | `bcq todos` |
| `/todo mine` | `bcq todos --assignee me` |
| `/todo "Deploy v2" --due friday` | `bcq todo "Deploy v2" --due friday` |

## Smart Defaults

- No project specified → use configured default
- "me" → resolves to current user
- Natural dates: today, tomorrow, friday, +3, next week
