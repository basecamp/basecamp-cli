---
name: basecamp-doctor
description: Diagnose Basecamp CLI, authentication, and agent-plugin health.
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

Report failures and warnings with their `hint` fields. Also inspect the top-level `breadcrumbs` array and preserve its structured `cmd` next steps, because a breadcrumb can provide a more specific action than a check hint. Use these common remediations when relevant:

- Basecamp authentication: `basecamp auth login`
- Agent plugin installation or version: `basecamp setup agents` (honors `BASECAMP_SETUP_AGENT`)
- Codex plugin specifically: `basecamp setup codex`
- Claude Code plugin specifically: `basecamp setup claude`

Every remediation above runs without a terminal. Bare `basecamp setup` is the
human first-time flow and is **not** one of them: it opens browser OAuth and
refuses with a usage error in machine-output modes or unless stdin, stdout, and
stderr are all terminals. `basecamp setup --customize` additionally asks the user to
choose each default. Suggest either to a human at a terminal if useful, but
never run them yourself — use the subcommands above.

Do not read, print, or request credential files. If every check passes, say that Basecamp and its agent integration are ready.
