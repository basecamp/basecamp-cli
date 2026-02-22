---
name: basecamp-api-reference
description: |
  Look up Basecamp 3 API endpoints, request/response formats, and usage examples.
  Use this to answer "how do I..." or "what's the endpoint for..." questions.
tools:
  - Bash
---

# Basecamp 3 API Reference

Answer questions about Basecamp API endpoints, shapes, and usage.

## Fetching Docs

Fetch the API README (cached locally):

```bash
DOCS_URL="https://raw.githubusercontent.com/basecamp/bc3-api/master/README.md"
CACHE_DIR="${HOME}/.cache/basecamp/api-docs"
README="$CACHE_DIR/README.md"

mkdir -p "$CACHE_DIR"
if [[ -f "$README" ]]; then
  curl -fsSL -z "$README" -o "$README" "$DOCS_URL" 2>/dev/null || true
else
  curl -fsSL -o "$README" "$DOCS_URL"
fi
```

## Find Endpoints

Search the README for endpoint references:

```bash
grep -n "todos\|todolists\|projects\|messages\|comments\|people" "$README"
```

Or with ripgrep (if available):

```bash
rg -n "todos|projects|messages" "$README"
```

## Fetch Section Docs

The README links to section files (e.g., `sections/todos.md`). Fetch them:

```bash
SECTION="sections/todos.md"
BASE_URL="https://raw.githubusercontent.com/basecamp/bc3-api/master"
SECTION_FILE="$CACHE_DIR/$SECTION"

mkdir -p "$(dirname "$SECTION_FILE")"
curl -fsSL -o "$SECTION_FILE" "$BASE_URL/$SECTION"
cat "$SECTION_FILE"
```

## Available Sections

| Resource | Section File |
|----------|-------------|
| Projects | `sections/projects.md` |
| Todos | `sections/todos.md` |
| Todolists | `sections/todolists.md` |
| Messages | `sections/messages.md` |
| Comments | `sections/comments.md` |
| People | `sections/people.md` |
| Campfires | `sections/campfires.md` |
| Recordings | `sections/recordings.md` |

## Base URL Pattern

All API endpoints use:

```
https://3.basecampapi.com/{account_id}/...
```

The `{account_id}` is the Basecamp account number (visible in Basecamp URLs).

## Authentication

All requests require:

```
Authorization: Bearer {access_token}
Content-Type: application/json
```

Use `basecamp auth status` to check current token, or `basecamp auth login` to authenticate.
