---
name: basecamp
description: |
  Interact with Basecamp through the Basecamp CLI. Use for any Basecamp question
  or action, including projects, todos, cards, messages, files, schedules,
  check-ins, people, assignments, notifications, search, and account settings.
triggers:
  - basecamp
  - /basecamp
  - basecamp todos
  - basecamp subtasks
  - basecamp project
  - basecamp cards
  - basecamp chat
  - basecamp campfire
  - basecamp messages
  - basecamp file
  - basecamp document
  - basecamp bookmarks
  - basecamp bubble-up
  - basecamp drafts
  - basecamp notes
  - basecamp calendars
  - basecamp schedule
  - basecamp checkin
  - basecamp check-in
  - basecamp timeline
  - basecamp template
  - basecamp webhook
  - basecamp gauge
  - basecamp assignment
  - basecamp notification
  - basecamp account
  - link to basecamp
  - track in basecamp
  - post to basecamp
  - comment on basecamp
  - complete todo
  - mark done
  - create todo
  - move card
  - download file
  - search basecamp
  - find in basecamp
  - look up basecamp
  - check basecamp
  - list basecamp
  - show basecamp
  - get from basecamp
  - fetch from basecamp
  - can I basecamp
  - how do I basecamp
  - what's in basecamp
  - what basecamp
  - does basecamp
  - my todos
  - my tasks
  - my schedule
  - my basecamp
  - assigned to me
  - my assignments
  - my notifications
  - overdue todos
  - upcoming events
  - project gauge
  - project progress
  - 3.basecamp.com
  - basecampapi.com
  - https://3.basecamp.com/
invocable: true
argument-hint: "[action] [args...]"
---

# Basecamp CLI

Use the installed CLI as the command reference. This skill defines the operating
rules; it intentionally does not duplicate the CLI's command catalog.

## Discovery-first workflow

For each task:

1. Infer the deepest plausible leaf command from the user's words.
2. If its exact arguments or flags are not already established by a tool result
   in this conversation, inspect that leaf directly:

   ```bash
   basecamp <group> <subcommand> --agent --help
   ```

3. If the leaf does not exist, inspect its nearest parent group. Use root help
   only when the top-level group itself is unclear:

   ```bash
   basecamp <group> --agent --help   # fallback
   basecamp --agent --help           # last resort
   ```

4. Treat leaf `usage`, `args`, `flags`, and `notes` as the source of truth. Do
   not guess positional arguments, flags, aliases, scope, or whether a group
   runs bare.
5. Execute with an explicit output mode, then read structured errors and
   breadcrumbs before deciding what to do next.

Targeted leaf help is cheap and local; root help returns the full command catalog
and is not token-cheap. Do not load root help merely to confirm global flags
already documented by this skill. A subcommand's `inherited_flags` is
intentionally short; global flags such as `--agent`, `--jq`, `--profile`, and
`--verbose` still apply where supported.

## Non-negotiable rules

- Never read, print, or log OAuth tokens or credential files. In particular, do
  not open `~/.config/basecamp/credentials.json`.
- Never pipe JSON to an external `jq`. Use the CLI's built-in `--jq`.
- Parse a supplied Basecamp URL before using IDs from it:
  `basecamp url parse "<url>" --json`. Only trust URLs from a known Basecamp
  host. Read-only commands whose leaf help explicitly accepts `<id|url>` may
  consume the trusted URL directly.
- Comments are flat. A reply is posted to the parent recording, never to a
  comment ID. For a comment URL, use `comments show` or `comments thread` to get
  `reply_target` when context matters.
- Respect project context. Check `.basecamp/config.json` before assuming a
  project; otherwise pass `--in <project>` or the scope shown by leaf help.
- Use non-interactive output for agent work. Do not allow an ambiguous picker to
  block execution.
- When a result reports content or description attachments, download and inspect
  them. Images often contain the essential task context.
- Follow the installed command's help over examples remembered from earlier CLI
  versions.

## Output modes

Choose deliberately:

| Goal | Mode |
|---|---|
| Extract, count, or transform data | `--jq '<expression>'` |
| Consume full structured response | `--json` |
| Consume data without an envelope | `--agent` |
| Present portable output directly to a person | `--md` |

`--jq` implies JSON. It normally filters the JSON envelope, so data is commonly
under `.data`. With `--agent --jq`, it filters the data-only payload. Use `--md`
for human-facing listings, but remember that `--md` alone does not suppress
interactive prompts. Add enough scope to remove ambiguity or set
`BASECAMP_NONINTERACTIVE=1`.

Examples of generic transformations, not command discovery:

```bash
basecamp <leaf-command> --jq '.data | length'
basecamp <leaf-command> --jq '[.data[] | {id, name}]'
```

## Project, account, and pagination scope

Do not infer that a command is project-scoped or account-wide from its noun.
Inspect the leaf help and its notes. Use the user's explicit project/account when
provided; otherwise honor trusted local configuration.

For paginated commands, inspect help before combining pagination flags. In
general, `--limit N` caps an auto-paginated result, `--all` walks every page,
and `--page N` requests one page, but individual endpoints can differ and
mutually exclusive combinations are rejected.

Named identities use profiles. Select one with global `--profile <name>` or
`BASECAMP_PROFILE=<name>`; do not silently switch profiles or default accounts.

## URLs and replies

A parsed URL can supply `account_id`, `project_id`, `recording_id`, and, when a
fragment points at a comment, `comment_id`.

```bash
basecamp url parse "https://3.basecamp.com/123/buckets/456/todos/789" --json
```

For replies, fetch reply-ready context rather than posting to the fragment ID:

```bash
basecamp comments thread "<comment-url>" --json
# Post to .data.reply_target.recording_id
```

Use `comments show` when only the cheap reply target and mention atoms are
needed; use `comments thread` when surrounding discussion matters.

## Content and stdin

Command help declares content arguments and stdin support. Titles are generally
plain text; rich-text fields generally accept Markdown and can opt into HTML
with `--format html` where documented. Do not guess a `--title`, `--body`, or
`--content` flag when help shows a positional argument.

For multiline, non-ASCII, or shell-sensitive content, pass `-` in the documented
content position and pipe stdin:

```bash
printf '%s\n' 'First line' '' 'Second line' |
  basecamp <leaf-command> <other-args> - --json
```

A pipe is never consumed implicitly. Only one input may read stdin in an
invocation. Do not use shell-specific ANSI-C quoting such as `$'line 1\nline 2'`
in portable commands.

For deterministic mentions, prefer the machine-provided mention syntax from
people or comment results, for example `[@Name](mention:SGID)`. Mention support
varies by field; leaf help is authoritative.

## Errors and recovery

Structured failures include `error`, `code`, `retryable`, and often `hint`.

- Retry only when `retryable` is true, with bounded backoff.
- `retryable: false` means there is no positive retry signal; it covers both
  known verdicts and unclassified failures. Inspect `code`, `error`, and `hint`
  before choosing recovery, and do not blindly repeat the same call.
- For usage errors, inspect leaf help and supply the named positional argument.
- For ambiguous names, use an ID or add the missing project/container scope.
- For auth or connectivity diagnosis, use `basecamp doctor --json` and
  `basecamp auth status --json` before changing credentials.
- Before any login recovery, inspect `oauth_type`. An `agent` profile must be
  recovered with the agent/client-credentials flow named by the CLI hint; a
  normal person login can replace the agent identity.

Do not improvise raw API calls merely because a command failed. First inspect the
relevant help and error hint. Use `basecamp api` only when the CLI has no native
operation and the API path and payload are known.

## Mutations

Before changing Basecamp:

1. Resolve the account, project, container, and item unambiguously.
2. Inspect leaf help for required positional arguments and visibility or
   notification behavior.
3. Make the smallest requested change.
4. Return the resulting ID/URL and summarize what changed.

For destructive or broad operations, preview when the command offers a dry-run
and do not expand the user's requested scope.
