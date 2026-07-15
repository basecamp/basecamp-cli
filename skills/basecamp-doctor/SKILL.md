---
name: basecamp-doctor
description: Diagnose Basecamp CLI, authentication, and Codex plugin health.
---

# Basecamp Doctor

Run the structured diagnostic:

```bash
basecamp doctor --json
```

Interpret every check by status:

- `pass`: working correctly.
- `warn`: usable, but follow-up is recommended.
- `skip`: not run because it is unauthenticated or not applicable.
- `fail`: broken and needs attention.

Report failures and warnings with their `hint` fields. Use these common remediations when relevant:

- Basecamp authentication: `basecamp auth login`
- Codex plugin installation or version: `basecamp setup codex`
- General CLI setup: `basecamp setup`

Do not read, print, or request credential files. If every check passes, say that Basecamp and its Codex integration are ready.
