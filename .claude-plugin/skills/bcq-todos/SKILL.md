---
name: bcq-todos
description: |
  Todo management with bcq. Create, list, complete, and comment on todos.
  Use for task tracking, work logging, and linking code to Basecamp todos.
triggers:
  - create todo
  - list todos
  - complete todo
  - mark done
  - todo list
  - assign todo
---

# bcq Todo Operations

Manage Basecamp todos via `bcq`.

## List Todos

```bash
# All todos in a project
bcq todos --in <project_id>

# In a specific todolist
bcq todos --in <project_id> --list <todolist_id>

# Filter by status
bcq todos --in <project_id> --status active
bcq todos --in <project_id> --status completed

# Filter by assignee
bcq todos --in <project_id> --assignee <person_id>
```

**Output:**
```
## Todos (12 todos)

| # | Content | Due | Status |
|---|---------|-----|--------|
| 123 | Fix authentication bug | 2025-01-15 | ○ |
| 124 | Update documentation | - | ✓ |
```

---

## Show Todo Details

```bash
bcq todos show <todo_id>
bcq todos <todo_id>
```

Shows content, status, due date, assignees, and description.

---

## Create Todo

```bash
# Basic
bcq todo "Fix the login bug"

# With project (required if no context)
bcq todo "Fix the login bug" --in <project_id>

# With todolist
bcq todo "Fix the login bug" --in <project_id> --list <todolist_id>

# With due date
bcq todo "Fix the login bug" --due 2025-01-15
```

**Output:**
```json
{
  "ok": true,
  "data": {"id": 67890, "content": "Fix the login bug", ...},
  "summary": "✓ Created todo #67890"
}
```

---

## Complete Todo

```bash
# Single todo
bcq done <todo_id>

# With explicit project
bcq done <todo_id> --project <project_id>

# Multiple todos
bcq done 123 456 789
```

**Output:**
```json
{
  "ok": true,
  "data": {"completed": ["123", "456"]},
  "summary": "✓ Completed 2 todo(s): 123 456"
}
```

---

## Add Comment to Todo

```bash
bcq comment "Comment text" --on <todo_id>
```

**Common patterns:**
```bash
# Link a commit
bcq comment "Fixed in $(git rev-parse --short HEAD)" --on 12345

# Link a PR
bcq comment "PR: https://github.com/org/repo/pull/42" --on 12345

# Add status update
bcq comment "Blocked: waiting on API changes" --on 12345
```

---

## Workflow Examples

### Track Work from Code

```bash
# 1. Create todo for the work
TODO_ID=$(bcq todo "Implement feature X" --in 12345 -q | jq -r '.id')

# 2. Do the work...

# 3. Link the commit
bcq comment "Implemented in $(git rev-parse --short HEAD)" --on $TODO_ID

# 4. Complete
bcq done $TODO_ID
```

### Find and Complete Related Todos

```bash
# Find todos mentioning "auth"
bcq todos --in 12345 -q | jq -r '.[] | select(.content | test("auth"; "i")) | .id'

# Complete all "done" todos
bcq todos --in 12345 -q | \
  jq -r '.[] | select(.content | contains("[done]")) | .id' | \
  xargs bcq done
```

### Daily Standup Check

```bash
# My incomplete todos
bcq todos --in 12345 --status active --assignee me

# Overdue todos
bcq todos --in 12345 -q | \
  jq -r '.[] | select(.due_on and .due_on < now | strftime("%Y-%m-%d") and .completed == false)'
```

---

## Project Context

Set defaults in `.basecamp/config.json`:

```json
{
  "project_id": 12345,
  "todolist_id": 67890
}
```

Then omit `--in` and `--list`:
```bash
bcq todos           # Uses default project
bcq todo "Task"     # Creates in default todolist
bcq done 123        # Completes in default project
```

---

## JSON Output

Use `-q` (quiet) for raw data without envelope:

```bash
# Get todo IDs
bcq todos --in 12345 -q | jq '.[].id'

# Get assignee info
bcq todos show 123 -q | jq '.assignees'

# Count by completion status
bcq todos --in 12345 -q | jq 'group_by(.completed) | map({done: .[0].completed, count: length})'
```
