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

- Basecamp authentication: `basecamp auth login`. Guard a replacement login
  with `--expect-identity <id>` so a browser signed in as someone else cannot
  become the profile. A bot or CI profile that must never sign in interactively
  imports a personal access token instead:
  `op read "op://<vault>/<item>/credential" | basecamp auth login --with-token -P <profile> --account <id> --expect-identity <id>`

  **Check `oauth_type` before suggesting either.** `basecamp auth status --json`
  reports it, and `agent` means the profile is a Basecamp agent: a principal
  with no person behind it, which authenticates with its OAuth client rather
  than a sign-in. Both commands above would store a PERSON's credential under
  that profile and silently replace the agent. Its remediation is its own
  login, with the client secret piped in:
  `op read "op://<vault>/<item>/credential" | basecamp auth login --with-client-credentials --client-id <id> -P <profile> --account <id>`
  The CLI's own `hint` on an agent credential already names this command with
  the client id filled in — prefer it verbatim over reconstructing one, and
  follow it rather than choosing for yourself whenever `oauth_type` is absent.
- Agent plugin installation or version: `basecamp setup agents` (honors `BASECAMP_SETUP_AGENT`)
- Codex plugin specifically: `basecamp setup codex`
- Claude Code plugin specifically: `basecamp setup claude`
- Grok skill specifically: `basecamp setup grok` (skill-only; Grok reads the shared `~/.agents/skills/basecamp` skill)

Every remediation above runs without a terminal. Bare `basecamp setup` is the
human first-time flow and is **not** one of them: it opens browser OAuth and
refuses with a usage error in machine-output modes or unless stdin, stdout, and
stderr are all terminals. `basecamp setup --customize` additionally asks the user to
choose each default. Suggest either to a human at a terminal if useful, but
never run them yourself — use the subcommands above.

Do not read, print, or request credential files. If every check passes, say that Basecamp and its agent integration are ready.
