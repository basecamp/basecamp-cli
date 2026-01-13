---
name: bcq-reference
description: |
  Command reference for bcq (Basecamp Query Tool). Use when needing to interact with
  Basecamp: list projects, manage todos, cards, campfire chat, add comments.
  Covers all bcq commands with examples and output patterns.
triggers:
  - basecamp
  - bcq
  - list projects
  - create todo
  - add comment
  - basecamp api
  - card table
  - kanban
  - campfire
  - chat message
---

# bcq Command Reference

`bcq` is an agent-first tool for the Basecamp API. All commands output structured JSON by default (for piping to jq) or human-readable markdown when run in a TTY.

## Quick Start

```bash
# Check authentication
bcq auth status

# List your projects
bcq projects

# List todos in a project
bcq todos --in <project_id>

# Create a todo
bcq todo "Fix the bug" --in <project_id>

# Complete a todo
bcq done <todo_id>
```

---

## Authentication

```bash
# Check auth status
bcq auth status

# Login via OAuth (opens browser)
bcq auth login

# Login with explicit scope for least-privilege
bcq auth login --scope read    # Read-only access
bcq auth login --scope full    # Full read+write access (default)

# Show current user
bcq me
```

Authentication uses OAuth 2.1 with DCR. Tokens are stored in `~/.config/basecamp/`.

**Scope options:**
- `full` (default): Read and write access to all resources
- `read`: Read-only access - cannot create, update, or delete

When requesting `full` scope, users can downgrade to `read` on the Basecamp consent screen.
If a read-only token attempts a write operation, bcq will show a clear error with re-auth hint.

---

## Projects

### List Projects

```bash
bcq projects
bcq projects list
bcq projects --status active
```

**Output (JSON):**
```json
{
  "ok": true,
  "data": [
    {"id": 12345, "name": "Security Triage", "updated_at": "2025-01-09T..."}
  ],
  "summary": "5 projects"
}
```

### Show Project

```bash
bcq projects <id>
bcq projects show <id>
```

Shows project details including enabled tools (dock items).

---

## Todos

### List Todos

```bash
# All todos in a project
bcq todos --in <project_id>

# Todos in a specific todolist
bcq todos --in <project_id> --list <todolist_id>

# Filter by status
bcq todos --in <project_id> --status completed
bcq todos --in <project_id> --status active

# Filter by assignee (use "me" for current user)
bcq todos --in <project_id> --assignee <person_id>
bcq todos --in <project_id> --assignee me
```

### Show Todo

```bash
bcq todos show <todo_id>
bcq todos <todo_id>
```

### Create Todo

```bash
bcq todo "Content here" --in <project_id>
bcq todo "Content here" --in <project_id> --list <todolist_id>
bcq todo "Content here" --in <project_id> --due 2025-01-15
bcq todo "Content here" --in <project_id> --due tomorrow
bcq todo "Content here" --in <project_id> --assignee me
```

**Date formats:** `today`, `tomorrow`, `+3` (3 days), `next week`, `YYYY-MM-DD`

**Output:**
```json
{
  "ok": true,
  "data": {"id": 67890, "content": "Content here", ...},
  "summary": "Created todo #67890"
}
```

### Complete Todo

```bash
bcq done <todo_id>
bcq done <todo_id> --project <project_id>

# Complete multiple
bcq done 123 456 789
```

---

## Cards (Card Tables)

Card Tables are Kanban-style boards with columns and cards.

### List Cards

```bash
bcq cards --in <project_id>
bcq cards --in <project_id> --column "In Progress"
```

### Create Card

```bash
bcq card "Card title" --in <project_id>
bcq card "Card title" --in <project_id> --column "Inbox"
```

### Move Card

```bash
bcq cards move <card_id> --to "Done"
```

### Show Card

```bash
bcq cards <card_id>
```

---

## Campfire (Chat)

Campfire is the real-time chat feature.

### Post Message

```bash
bcq campfire post "Hello team!" --in <project_id>
bcq campfire <campfire_id> post "Message here"
```

### View Messages

```bash
bcq campfire messages --in <project_id>
bcq campfire <campfire_id> messages --limit 50
```

### Thought Stream Pattern

```bash
# Narrate work in progress
bcq campfire post "[10:30] Starting security triage. 7 reports in inbox."
bcq campfire post "[10:35] Processing #3481234 - confirmed SSRF"
bcq campfire post "[10:45] ✓ Complete. VALID - Tier 4 ($500)"
```

---

## Comments

Add comments to any Basecamp recording (todo, message, document, card, etc.).

```bash
bcq comment "Comment text" --on <recording_id>
bcq comment "Comment text" --on <recording_id> --project <project_id>
```

**Examples:**
```bash
# Link a commit to a todo
bcq comment "Fixed in commit abc123" --on 12345

# Link a PR
bcq comment "PR: https://github.com/org/repo/pull/42" --on 12345
```

---

## People

```bash
# List all people
bcq people

# People on a specific project
bcq people --project <project_id>

# Show person details
bcq people show <person_id>

# List pingable people (can receive direct messages)
bcq people pingable
```

---

## Output Formats

All commands support format flags:

```bash
# JSON (default when piped)
bcq projects --json
bcq projects -j

# Markdown (default in TTY)
bcq projects --md
bcq projects -m

# Data only (raw JSON, no envelope)
bcq projects --quiet
bcq projects -q
```

### JSON Envelope

All JSON output follows this structure:

```json
{
  "ok": true,
  "data": { ... },
  "summary": "Human-readable summary",
  "breadcrumbs": [
    {"action": "next", "cmd": "bcq ...", "description": "Suggested next command"}
  ]
}
```

Error responses:
```json
{
  "ok": false,
  "error": "Error message",
  "code": "error_code",
  "hint": "Suggestion to fix"
}
```

---

## Project Context

bcq can read project context from `.basecamp/config.json`:

```json
{
  "project_id": 12345,
  "todolist_id": 67890
}
```

With context set, you can omit `--in` flags:
```bash
bcq todos          # Uses project from config
bcq todo "Task"    # Creates in default project/todolist
```

---

## Piping with jq

bcq output is designed for jq processing:

```bash
# Get project IDs
bcq projects -q | jq '.[].id'

# Find todos containing "bug"
bcq todos --in 12345 -q | jq '.[] | select(.content | contains("bug"))'

# Get todo count by status
bcq todos --in 12345 -q | jq 'group_by(.completed) | map({completed: .[0].completed, count: length})'
```

---

## Common Patterns

### Link Code to Basecamp

```bash
# Comment with commit info
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
bcq comment "Commit $COMMIT: $MSG" --on $TODO_ID

# Comment with PR link
bcq comment "PR: $PR_URL" --on $TODO_ID
```

### Bulk Operations

```bash
# Complete all todos matching pattern
bcq todos --in 12345 -q | \
  jq -r '.[] | select(.content | contains("done")) | .id' | \
  xargs bcq done
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Usage error |
| 2 | Not found |
| 3 | Auth required |
| 4 | Forbidden |
| 5 | Rate limited |
| 6 | Network error |
| 7 | API error |
