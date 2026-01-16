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

Use the helper script to get the README path:

```bash
README="$(./scripts/api-docs.sh)"
```

Then read it to find endpoint links:

```bash
cat "$README"
```

## Find the Right Doc

Don't assume section paths — they can change. Use ripgrep to locate the section:

```bash
README="$(./scripts/api-docs.sh)"
rg -n "todos" "$README"
```

Then open the linked section:

```bash
DOCS_DIR="$(dirname "$README")"
cat "$DOCS_DIR/sections/todos.md"
```

## Base URL Pattern

All endpoints use:
```
https://3.basecampapi.com/{account_id}/...
```

The `{account_id}` is the Basecamp account number.
