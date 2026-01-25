# Basecamp API with Agent Guide

Direct Basecamp API access via curl + jq. No CLI wrapper.

## Canonical Paths (USE EXACTLY)

These are the only valid URL patterns. **Never invent or combine paths.**

| Operation | Method | Path |
|-----------|--------|------|
| List projects | GET | `/projects.json` |
| Get project | GET | `/projects/{project}.json` |
| List todolists | GET | `/buckets/{project}/todosets/{todoset}/todolists.json` |
| List todos | GET | `/buckets/{project}/todolists/{list}/todos.json` |
| Complete todo | POST | `/buckets/{project}/todos/{id}/completion.json` |
| Get comments | GET | `/buckets/{project}/recordings/{id}/comments.json` |
| Post comment | POST | `/buckets/{project}/recordings/{id}/comments.json` |

**URL Assembly Rule**: Paths are exact. Never combine segments from different endpoints. If unsure about a path, fetch the parent object to get the correct URL from its response.

## Agent Invariants (READ FIRST)

These rules prevent common failures:

1. **Paginate until EMPTY, not until target found** — Finding your target does NOT stop pagination. Always fetch page N+1 until the response is `[]` (empty array). This ensures you don't miss items on later pages.
2. **POST requires JSON body** — Every POST/PUT request must include a JSON body. Sending POST without body returns 422.
3. **Use canonical paths only** — See path table above. Do not invent URLs like `/todosets/.../todolists/.../todos.json`.
4. **bucket = project** — In all URLs, `bucket_id` means project ID.
5. **Find todoset via project dock** — GET project, find `dock[]` entry with `name: "todoset"`, use that ID.
6. **Overdue = due_on < today AND not completed** — Filter client-side after fetching todos.
7. **Idempotent comments** — Before posting, check existing comments. If ANY comment contains "BenchChain", do NOT post another. Skip directly to completing the todo.

## Pagination (USE paginate:true)

For any list endpoint, use `paginate: true` to auto-fetch all pages:

```json
http_request({
  "method": "GET",
  "path": "/buckets/{project}/todolists/{list}/todos.json",
  "paginate": true
})
```

Response:
```json
{
  "status": 200,
  "items": [...all items from all pages...],
  "pages_fetched": 3,
  "next_page": null
}
```

**Always use `paginate: true` for list endpoints.** This eliminates manual pagination loops and ensures all items are fetched.

## Request Body Requirements

| Method | Body Required | Example |
|--------|---------------|---------|
| GET | No | - |
| POST | **YES** | `{"content": "..."}` |
| PUT | **YES** | `{"content": "..."}` |
| DELETE | No | - |

### Required POST Bodies for Common Endpoints

**Create comment:**
```json
POST /buckets/{project}/recordings/{id}/comments.json
{"content": "BenchChain - processed by agent"}
```

**Create todo:**
```json
POST /buckets/{project}/todolists/{list}/todos.json
{"content": "Todo description", "due_on": "2024-01-15", "assignee_ids": [123]}
```

**Complete todo:** (no body needed - state change only)
```
POST /buckets/{project}/todos/{id}/completion.json
```

## Error Recovery

| Status | Action |
|--------|--------|
| 401 | Token expired. Cannot recover without re-auth. |
| 422 | Missing or invalid body. Check `error` and `hint` fields. Always send JSON body for POST/PUT. |
| 429 | Read `Retry-After` header, wait that many seconds, retry same request. |

**429 handling pattern:**
```bash
if [[ "$http_code" == "429" ]]; then
  retry_after=$(grep -i "Retry-After" <<< "$headers" | awk '{print $2}')
  sleep "${retry_after:-2}"
  # Retry the same request
fi
```

## Authentication

```bash
# Environment provides:
# - BASECAMP_TOKEN: Bearer token
# - BCQ_API_BASE: Full base URL including account ID

# All requests require:
# Authorization: Bearer $BASECAMP_TOKEN
# Content-Type: application/json
```

## Common Flows

### List projects
```bash
curl -s "$BCQ_API_BASE/projects.json" -H "Authorization: Bearer $BASECAMP_TOKEN"
```

### Get project (to find todoset ID from dock)
```bash
curl -s "$BCQ_API_BASE/projects/{project_id}.json" -H "Authorization: Bearer $BASECAMP_TOKEN"
# Response includes: dock: [{name: "todoset", id: 123}, ...]
```

### List todolists in a project
```bash
curl -s "$BCQ_API_BASE/buckets/{project_id}/todosets/{todoset_id}/todolists.json" \
  -H "Authorization: Bearer $BASECAMP_TOKEN"
```

### List todos in a todolist (with pagination)
```bash
page=1
while true; do
  items=$(curl -s "$BCQ_API_BASE/buckets/{project_id}/todolists/{todolist_id}/todos.json?page=$page" \
    -H "Authorization: Bearer $BASECAMP_TOKEN")
  [[ $(echo "$items" | jq 'length') -eq 0 ]] && break
  # Process items...
  ((page++))
done
```

### Complete a todo
```bash
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/todos/{todo_id}/completion.json" \
  -H "Authorization: Bearer $BASECAMP_TOKEN"
```

### Post a comment
```bash
curl -s -X POST "$BCQ_API_BASE/buckets/{project_id}/recordings/{recording_id}/comments.json" \
  -H "Authorization: Bearer $BASECAMP_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content": "BenchChain - processed by agent"}'
```

## Task-Specific: Finding Overdue Todos

To find and complete overdue todos across projects:

1. GET /projects.json — list all projects
2. For each project, GET /projects/{id}.json — find todoset ID in dock
3. GET /buckets/{project}/todosets/{todoset}/todolists.json — list all todolists
4. For each todolist:
   - **Use `paginate: true`** to fetch all todos in one call
   - Filter results for `due_on < today AND completed == false`
5. For each overdue todo, **complete all three steps in order**:
   1. **Check comments**: GET /buckets/{project}/recordings/{todo_id}/comments.json
   2. **Post marker** (only if not already present): POST with `{"content": "BenchChain - processed by agent"}`
   3. **Complete todo**: POST /buckets/{project}/todos/{todo_id}/completion.json

**Completion is required** — even if commenting succeeds, the todo must be marked complete.
