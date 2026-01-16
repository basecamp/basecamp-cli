---
name: basecamp-api-reference
description: |
  Look up Basecamp 3 API endpoints, request/response formats, and usage examples.
  Use this to answer "how do I..." or "what's the endpoint for..." questions.
tools:
  - Bash
  - Read
---

# Basecamp 3 API Reference

Answer questions about Basecamp API endpoints, shapes, and usage.

## Fetching Docs

Use the helper script to get the README path:

```bash
README="$(./scripts/api-docs.sh)"
```

Then read it to find endpoint links:

```bash
cat "$README"
```

## Documentation Structure

The README links to individual endpoint docs:

| Resource | Doc file |
|----------|----------|
| Projects | `sections/projects.md` |
| Todos | `sections/todos.md` |
| Todolists | `sections/todolists.md` |
| Todosets | `sections/todosets.md` |
| Messages | `sections/messages.md` |
| Message Boards | `sections/message_boards.md` |
| Comments | `sections/comments.md` |
| People | `sections/people.md` |
| Campfires | `sections/campfires.md` |
| Recordings | `sections/recordings.md` |

To read a specific doc, derive its path from the README location:

```bash
README="$(./scripts/api-docs.sh)"
DOCS_DIR="$(dirname "$README")"
cat "$DOCS_DIR/sections/todos.md"
```

## Common Questions

**"How do I complete a todo?"**
→ Read `sections/todos.md`, look for completion endpoint

**"What fields does a project have?"**
→ Read `sections/projects.md`, look at response example

**"How do I add a comment?"**
→ Read `sections/comments.md`

## Base URL Pattern

All endpoints use:
```
https://3.basecampapi.com/{account_id}/...
```

The `{account_id}` is the Basecamp account number.
