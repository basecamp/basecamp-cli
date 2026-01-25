# Eval Case Schema

## Fixture Format (Method-Scoped)

Fixtures are a **list** to avoid duplicate key issues. Each fixture specifies method explicitly.

```yaml
fixtures:
  # GET /projects.json
  - method: GET
    path: "/projects.json"
    response:
      status: 200
      body: [{id: 1, name: "Project"}]

  # GET /todos.json with pagination
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "1"}
    response:
      status: 200
      body: [{id: 1001, content: "Todo 1"}]

  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "2"}
    response:
      status: 200
      body: []

  # POST /completion.json
  - method: POST
    path: "/buckets/1/todos/1001/completion.json"
    response:
      status: 200
      body: {id: 1001, completed: true}
```

## Matching Rules

### Specificity-Based Matching (Most-Specific Wins)

Fixtures are matched by **specificity score**, not list order:

| Component | Score |
|-----------|-------|
| method match | required |
| path match | required |
| query match (exact) | +2 |
| body match (exact) | +1 |

**Method and path must match** for a fixture to be eligible. Query and body add specificity score.

**Highest score wins.** Ties break by list order (first wins).

**Body mismatch = ineligible.** If a fixture specifies `body`, the request body must match structurally or the fixture is skipped entirely (not just scored lower).

This prevents queryless fixtures from shadowing query-specific ones:

```yaml
fixtures:
  # Catch-all for unknown pages (score: 2 = method + path)
  - method: GET
    path: "/todos.json"
    response:
      status: 200
      body: []

  # Specific page 1 (score: 4 = method + path + query)
  - method: GET
    path: "/todos.json"
    query: {page: "1"}
    response:
      status: 200
      body: [{id: 1}]
```

Request `GET /todos.json?page=1` → matches page-1 fixture (score 4 > 2).
Request `GET /todos.json?page=99` → matches catch-all (only fixture that matches).

### Path Normalization

Paths are normalized for matching:

1. **Strip leading/trailing slashes**: `/foo/bar/` → `foo/bar`
2. **Case-sensitive**: `/Projects.json` ≠ `/projects.json` (no lowercasing)
3. **Full URLs extracted**: Path and query parsed separately
   - `https://api.example.com/foo.json?page=2` → path: `foo.json`, query: `{page: "2"}`
   - Query params from URL are normalized identically to explicit query params

### Query Normalization

All query values coerced to strings, sorted by key:

```yaml
# These are equivalent after normalization:
query: {page: 2}
query: {page: "2"}
query: {"page": "2"}
```

**Array parameters** use bracket notation with sorted values:

```yaml
# Request: ?type[]=Todo&type[]=Message
# Normalized: {type[]: ["Message", "Todo"]}  # sorted alphabetically
query:
  "type[]": ["Message", "Todo"]
```

**Repeated keys in requests** (model sends `type=Todo&type=Message` without brackets):
- Normalized to array format: `{type: ["Message", "Todo"]}` (sorted)
- Matches fixture with `type: ["Message", "Todo"]` or `"type[]": ["Message", "Todo"]`

**Repeated keys in case definitions:** Ruby's YAML parser silently takes the last value for duplicate keys. This is a known limitation—avoid duplicate keys in fixture/assertion definitions. Lint your YAML separately if needed.

### Body Matching

For `body_contains` assertions:

- Applied to **JSON-serialized string** (compact, sorted keys)
- Substring match, case-sensitive
- Example: `body_contains: "BenchChain"` matches `{"content": "Processed BenchChain abc123"}`

For exact body matching in fixtures (optional):

```yaml
- method: POST
  path: "/comments.json"
  body: {content: "exact match required"}  # Request body must match
  response: ...
```

Body comparison uses **structural equality** (key order ignored, values must match).

## Error Injection

Injection is scoped by **method + path + query** and tracks call count per scope.

```yaml
inject:
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "2"}
    on_call: 1              # 1 = first call to this method+path+query
    response:
      status: 429
      headers:
        Retry-After: "2"
      body: {error: "Rate limited"}
```

**Scope:** `on_call` counts calls to the exact `method + path + normalized_query` combination.

**Injection takes precedence** over fixtures when `on_call` matches.

## Assertions

### `required_sequence`

Calls that MUST happen, in order. Other calls allowed between unless `strict: true`.

```yaml
assertions:
  required_sequence:
    - method: GET
      path: "/projects.json"
      expect_status: 200

    - method: GET
      path: "/todos.json"
      query: {page: "2"}
      occurrence: 1               # First call to this endpoint
      expect_status: 429          # Assert it got rate limited

    - method: GET
      path: "/todos.json"
      query: {page: "2"}
      occurrence: 2               # Second call (retry)
      expect_status: 200          # Assert retry succeeded

  # Optional: fail if ANY calls happen between sequence steps
  strict: false  # default; applies only to required_sequence
```

**`occurrence`:** Which call to this method+path+query (1-indexed). Defaults to "any matching call."

**`expect_status`:** Assert the response status for this call. Critical for testing error handling.

**`strict`:** When true, no calls allowed between sequence steps. Applies only to `required_sequence`.

### `required_any` (Alternative Paths)

When multiple valid workflows exist, use `required_any` to accept either:

```yaml
assertions:
  required_any:
    - method: GET
      path: "/projects.json"      # Agent might list all projects
    - method: GET
      path: "/projects/1.json"    # Or fetch single project directly
```

**`method` is required** for each alternative. Passes if **at least one** listed call occurs.

**Note:** `required_any` only matches `method`, `path`, and `query`. Status assertions (`expect_status`) are not supported here—use `required_sequence` for status validation.

### `forbidden`

Patterns that MUST NOT occur.

```yaml
assertions:
  forbidden:
    - method: POST
      path: "/comments.json"
      body_contains: "BenchChain"
      max_count: 0                # default: 0 (must not occur)

    - method: GET
      path: "/projects.json"
      max_count: 2                # At most 2 calls (don't refetch excessively)
```

### `end_state`

Final count conditions on request log.

```yaml
assertions:
  end_state:
    - method: POST
      path: "/completion.json"
      count: 1                    # Exactly 1

    - method: POST
      path: "/comments.json"
      count: 1
      body_contains: "BenchChain" # Must contain marker
```

### `max_calls` (Hard Limit)

Cap total HTTP calls. **Hard fail** if exceeded—evaluation stops immediately.

```yaml
assertions:
  max_calls: 20  # Fail immediately if call 21 attempted
```

This catches runaway loops before they burn tokens.

## Unknown Endpoints

Requests to paths not in fixtures return:
- `404 Not Found` with body `{"error": "Fixture not found", "path": "<requested_path>"}`

This lets `forbidden` assertions catch unexpected calls without terminating the run.

## Notes (Non-Enforced)

Human-readable summary for documentation. **Not programmatically evaluated.**

```yaml
notes:
  - "Fetched all 3 pages (including empty page 3)"
  - "Completed exactly 1 overdue todo"
```

Previously called `pass_criteria`; renamed to clarify it's prose, not assertions.

## Output Format

```
[pagination_full_traversal] PASS
  ✓ required_sequence: 5/5 calls
  ✓ required_any: 1/2 alternatives matched
  ✓ forbidden: 0 violations
  ✓ end_state: 2/2 conditions
  ✓ max_calls: 8 (limit: 20)

[retry_429] FAIL
  ✓ required_sequence: 3/4 calls
  ✗ FAIL: GET /todos.json?page=2 occurrence=1 expected status 429, got 200
  - forbidden: 0 violations
  - end_state: not evaluated (sequence failed)
```

## Full Example

```yaml
name: retry_429_with_pagination
description: Test pagination + rate limit recovery

fixtures:
  - method: GET
    path: "/projects/1.json"
    response:
      status: 200
      body:
        id: 1
        dock: [{name: "todoset", id: 10}]

  - method: GET
    path: "/buckets/1/todosets/10/todolists.json"
    response:
      status: 200
      body: [{id: 100, name: "Main"}]

  # Catch-all for unknown pages (returns empty)
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    response:
      status: 200
      body: []

  # Page 1 (more specific, wins over catch-all)
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "1"}
    response:
      status: 200
      body: [{id: 1001, content: "Todo", due_on: null}]

  # Page 2 (more specific, wins over catch-all)
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "2"}
    response:
      status: 200
      body: [{id: 1003, content: "Overdue", due_on: "2020-01-01"}]

  # Page 3 (explicit empty)
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "3"}
    response:
      status: 200
      body: []

  - method: POST
    path: "/buckets/1/todos/1003/completion.json"
    response:
      status: 200
      body: {completed: true}

inject:
  - method: GET
    path: "/buckets/1/todolists/100/todos.json"
    query: {page: "2"}
    on_call: 1
    response:
      status: 429
      headers: {Retry-After: "2"}
      body: {error: "Rate limited"}

assertions:
  required_sequence:
    - method: GET
      path: "/buckets/1/todolists/100/todos.json"
      query: {page: "1"}
      expect_status: 200

    - method: GET
      path: "/buckets/1/todolists/100/todos.json"
      query: {page: "2"}
      occurrence: 1
      expect_status: 429

    - method: GET
      path: "/buckets/1/todolists/100/todos.json"
      query: {page: "2"}
      occurrence: 2
      expect_status: 200

    - method: GET
      path: "/buckets/1/todolists/100/todos.json"
      query: {page: "3"}
      expect_status: 200

  end_state:
    - method: POST
      path: "/buckets/1/todos/1003/completion.json"
      count: 1

  max_calls: 15

notes:
  - "First page 2 attempt gets 429"
  - "Agent retries after Retry-After delay"
  - "Pagination continues through empty page 3"
  - "Overdue todo completed (proves full flow)"
```
