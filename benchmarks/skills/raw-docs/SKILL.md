---
name: raw-docs
version: "0.1.0"
description: Basecamp API via curl (docs reference only)
tools:
  - Bash
---

# Basecamp API (Raw)

Direct Basecamp 4 API access via curl + jq. No CLI wrapper.

## Documentation

Fetch the official API documentation:
```bash
curl -s https://raw.githubusercontent.com/basecamp/bc3-api/master/README.md
```

## Authentication

```bash
export BASECAMP_ACCESS_TOKEN="your_token"
export BASECAMP_ACCOUNT_ID="your_account_id"

# All requests require:
# Authorization: Bearer $BASECAMP_ACCESS_TOKEN
# Content-Type: application/json
# User-Agent: YourApp (you@example.com)
```

## Base URL

```
https://3.basecampapi.com/{account_id}/
```

Consult the API documentation for endpoints, request formats, and response structures.
