---
name: raw-guided
version: "0.1.0"
description: Basecamp API via curl with endpoint guidance
tools:
  - Bash
---

# Basecamp API (Raw + Guidance)

Direct Basecamp API access via curl + jq. No CLI wrapper.
Includes endpoint examples and domain patterns.

## Authentication

```bash
# Environment provides:
# - BCQ_ACCESS_TOKEN: Bearer token
# - BCQ_API_BASE: Full base URL including account ID

# All requests require:
# Authorization: Bearer $BCQ_ACCESS_TOKEN
# Content-Type: application/json
# User-Agent: YourApp (you@example.com)
```

## Common Endpoints

### Projects

```bash
# List all projects
curl -s "$BCQ_API_BASE/projects.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq

# Get project details
curl -s "$BCQ_API_BASE/projects/{project_id}.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq
```

### Todos

```bash
# List todos in a todolist
curl -s "$BCQ_API_BASE/buckets/{project_id}/todolists/{todolist_id}/todos.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq

# Create a todo
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/todolists/{todolist_id}/todos.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content": "Todo text", "assignee_ids": [123]}' | jq

# Complete a todo
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/todos/{todo_id}/completion.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN"
```

### Todolists

```bash
# List todolists in a project's todoset
curl -s "$BCQ_API_BASE/buckets/{project_id}/todosets/{todoset_id}/todolists.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq

# Create a todolist
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/todosets/{todoset_id}/todolists.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "List name"}' | jq
```

### Messages

```bash
# List messages
curl -s "$BCQ_API_BASE/buckets/{project_id}/message_boards/{board_id}/messages.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq
```

### Comments

```bash
# Add comment to any recording
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/recordings/{recording_id}/comments.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content": "Comment text"}' | jq
```

### People

```bash
# List all people
curl -s "$BCQ_API_BASE/people.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq

# Get person by ID
curl -s "$BCQ_API_BASE/people/{person_id}.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq
```

### Search

```bash
# Search across projects
curl -s "$BCQ_API_BASE/projects/search.json?query=term" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq
```

## Pagination

Responses with many items include a `Link` header:

```
Link: <https://3.basecampapi.com/.../todos.json?page=2>; rel="next"
```

Fetch all pages by following `rel="next"` links.

## Rate Limiting

- 50 requests per 10 seconds per token
- 429 response includes `Retry-After` header
- Back off and retry on 429

## Response Structure

### Project

```json
{
  "id": 123,
  "name": "Project Name",
  "dock": [
    {"name": "todoset", "id": 456, "enabled": true},
    {"name": "message_board", "id": 789, "enabled": true}
  ]
}
```

### Todo

```json
{
  "id": 123,
  "content": "Todo text",
  "completed": false,
  "due_on": "2025-01-20",
  "assignees": [{"id": 456, "name": "Person"}],
  "parent": {"id": 789, "type": "Todolist"}
}
```

## URL Patterns

All recording URLs follow:
```
/buckets/{project_id}/...
```

The `bucket_id` in responses is always the project ID.

## Notes

- `todoset` is the container, `todolist` is the actual list
- Check project `dock[]` for available features
- `assignees` is always an array, even for single assignment
- Use `?status=active` or `?status=completed` to filter todos
