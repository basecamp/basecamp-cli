---
name: raw-docs
version: "0.1.0"
description: Basecamp API via curl (docs reference only)
tools:
  - Bash
---

# Basecamp API (Raw)

Direct Basecamp API access via curl + jq. No CLI wrapper.

## Documentation

Fetch the official API documentation:
```bash
curl -s https://raw.githubusercontent.com/basecamp/bc3-api/master/README.md
```

## Authentication

```bash
# Environment provides:
# - BASECAMP_TOKEN: Bearer token
# - BCQ_API_BASE: Full base URL including account ID

# All requests require:
# Authorization: Bearer $BASECAMP_TOKEN
# Content-Type: application/json
# User-Agent: YourApp (you@example.com)
```

Consult the API documentation for endpoints, request formats, and response structures.
