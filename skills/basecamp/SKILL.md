---
name: basecamp
description: |
  Interact with Basecamp via the Basecamp CLI. Full API coverage: projects, todos, cards,
  messages, files, schedule, check-ins, timeline, recordings, templates, webhooks,
  subscriptions, lineup, chat, pings, gauges, assignments, notifications, bookmarks,
  bubble-up, drafts, notes, calendars, and accounts.
  Use for ANY Basecamp question or action.
triggers:
  # Direct invocations
  - basecamp
  - /basecamp
  # Resource actions
  - basecamp todos
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
  # Common actions
  - link to basecamp
  - track in basecamp
  - post to basecamp
  - comment on basecamp
  - complete todo
  - mark done
  - create todo
  - move card
  - download file
  # Search and discovery
  - search basecamp
  - find in basecamp
  - look up basecamp
  - check basecamp
  - list basecamp
  - show basecamp
  - get from basecamp
  - fetch from basecamp
  # Questions
  - can I basecamp
  - how do I basecamp
  - what's in basecamp
  - what basecamp
  - does basecamp
  # My work
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
  # URLs
  - 3.basecamp.com
  - basecampapi.com
  - https://3.basecamp.com/
invocable: true
argument-hint: "[action] [args...]"
---

# /basecamp - Basecamp Workflow Command

Full CLI coverage: 189 tracked in-scope endpoints across todos, cards, messages, files, schedule, check-ins, timeline, recordings, templates, webhooks, subscriptions, lineup, chat, pings, gauges, assignments, notifications, and accounts.

## Agent Invariants

**MUST follow these rules:**

1. **Choose the right output mode** — `--jq` when you need to filter/extract data; `--json` for full JSON; `--md` when presenting results to a human (see Output Modes below). **Never pipe to external `jq` — use `--jq` instead.**
2. **Parse URLs first** with `basecamp url parse "<url>"` to extract IDs
3. **Comments are flat** - reply to parent recording, not to comments
4. **Check context** via `.basecamp/config.json` before assuming project
5. **Content fields accept Markdown and @mentions** — message body and comment content accept Markdown syntax; the CLI converts to HTML automatically. Use Markdown formatting (lists, bold, links, code blocks, tables) for rich content. Four mention syntaxes are available (prefer deterministic for agents):
   - **`[@Name](mention:SGID)`** — zero API calls, embeds SGID directly (preferred for agents)
   - **`[@Name](person:ID)`** — one API call, resolves person ID to SGID via pingable set
   - **`@sgid:VALUE`** — inline SGID embed for pipeline composability
   - **`@Name` / `@First.Last`** — fuzzy name resolution (may be ambiguous)
   For todos, documents, and cards, content is sent as-is — use plain text or HTML directly.

   **Table boundary:** GFM tables round-trip: they render in message/comment
   bodies, display converts them back to pipe tables, and the TUI in-place
   editors open simple grids for editing. Only **complex** tables — merged
   cells (colspan/rowspan), captions, extra header rows, nested tables,
   attachments/images or block content inside cells, multi-paragraph or
   multi-line cells, or a table inside a blockquote or list — refuse to open,
   since a GFM pipe table can't represent those shapes (edit them on Basecamp
   web, or replace the
   whole field via `messages update` / `comments update` / `todos update
   --description`, which take fresh content and are unaffected). Complex
   tables still **display** best-effort, flattened to a plain grid.

   **Multiline / non-ASCII content:** do not rely on bash ANSI-C quoting (`$'...\n...'`) — it is a bash/zsh extension. Under a POSIX `/bin/sh` (dash, busybox-ash, common in sandboxes) the `$` is passed through literally and posts a stray leading `$`, and `\n` stays a literal backslash-n. Pipe the content via stdin instead, using `-` as the content argument:
   ```bash
   printf '%s\n' '海报 mockup 方向稿：' '' '<bc-attachment ...>' | basecamp comments create <recording_id> - --in <project> --json
   ```
   `-` means "read from stdin" on every content input: content-kind positionals
   (`comments create/update`, `messages create [body]`, `cards create [body]`,
   `todos create`, `docs documents create [content]`, `chat post/update`, `boost create`,
   `checkins answer create/update`, `notes set`) and content flags (`--data` on
   `api post/put`, `--body`, `--content`, `--description`, `--comment` on
   `todos sweep`, `--file` on `notes set`). Each command's `--agent` help lists
   its stdin inputs. Rules:
   - A pipe is **never consumed implicitly** — without `-` it is ignored (or, where
     content is required and missing, the error teaches `-`).
   - Only one input can read stdin per invocation.
   - A literal `-` anywhere else (a title, a name, a path) **errors when stdin is
     piped**. Escape a positional after the `--` separator
     (`basecamp projects create -- -`); a flag value has no in-line escape — run
     the command without piped stdin. `basecamp help` and shell completion are
     exempt: they write nothing to Basecamp, and completion legitimately
     receives `-` as the word being completed.
   - `-` with nothing piped (interactive TTY) errors immediately instead of
     hanging; use a pipe, a heredoc (`basecamp comments create <id> - <<'EOF'`),
     or `--edit` where offered.
   - Trailing newlines are trimmed from stdin content, so `printf 'x\n' | ... -`
     posts `x` (this keeps `boost create -` inside its 16-rune limit).
6. **Project scope is mandatory for most commands** — via `--in <project>` or `.basecamp/config.json`. Cross-project exceptions: `basecamp reports assigned` for assigned work, `basecamp assignments` for structured assignment views, `basecamp reports overdue` for overdue todos, `basecamp reports schedule` for upcoming schedule across all projects, `basecamp recordings <type>` for browsing by type, `basecamp notifications` for notifications, `basecamp gauges list` for account-wide gauges, and the seven list commands covered in item 7.
7. **Account-wide listing.** `basecamp todos list --all-projects --json` lists across every project; the same flag does the same on `cards list`, `messages list`, `comments list`, `files list`, `forwards list`, and `checkins answers`. It overrides a configured project, and with no project in scope those commands already list account-wide rather than prompting. Flags that name something inside a single project are rejected there rather than silently ignored.
   Account-wide listings return **the first 100 items by default** — account-wide "all" is the whole account, not one project's worth. Use `--limit N` to raise the cap (it walks pages until N are collected) or `--all` for everything. `--page N` fetches exactly one page, but only on the paginated listings.
   The two overdue variants — `basecamp todos list --all-projects --overdue` and `basecamp cards list --all-projects --overdue` — come from unpaginated endpoints. They accept `--limit` and `--all` but **reject `--page`**, so do not generate `--page` against them.

### Output Modes

**Choosing a mode:**

| Goal | Flag | Format |
|------|------|--------|
| Filter/extract JSON data | `--jq '<expr>'` | Built-in jq filter (no external jq needed). Implies `--json`; filter runs on the envelope. |
| Filter in agent mode | `--agent --jq '<expr>'` | Filter runs on data-only payload (no envelope), matching `--agent` contract. |
| Full JSON output | `--json` | JSON envelope: `{ok, data, summary, breadcrumbs, meta}`; errors: `{ok:false, error, code, retryable, hint, meta}` |
| Show results to a user | `--md` / `-m` | GFM tables, task lists, structured Markdown |
| Automation / scripting | `--agent` | Success: raw JSON data (no envelope); errors: `{ok:false,...}` object; no interactive prompts |

Always pass `--json` or `--md` explicitly — auto-detection depends on config and may not produce the format you expect. Use `--md` when composing reports, summarizing data, or displaying results inline. `--agent` is for headless integration scripts.

**Avoiding interactive prompts.** The flags `--agent`/`--json`/`--quiet`/`--ids-only`/`--count` and the environment variable `BASECAMP_NONINTERACTIVE=1` suppress interactive selection prompts. `--md` does **not** — if a required target is ambiguous (e.g. a project with multiple todosets and no `--todoset`), and the CLI is attached to a terminal, it will show a blocking picker. When you need Markdown output *and* no prompts, either pass the flag that names whatever is ambiguous (`--todoset <id>` for the todoset case above, or `--in <project>` / `--list <id>` when the project or list is ambiguous) or set `BASECAMP_NONINTERACTIVE=1` in the environment. `BASECAMP_NONINTERACTIVE` disables all prompts (they become actionable errors instead) without changing the output format — an escape hatch for agents running under a PTY.

**Other modes:** `--quiet` (success: raw JSON, no envelope; errors: `{ok:false,...}`), `--ids-only`, `--count`, `--stats` (session statistics), `--styled` (force ANSI), `-v` / `-vv` (verbose/trace), `--jq '<expr>'` (built-in jq filter — see below).

### CLI Introspection

Navigate unfamiliar commands with `--agent --help` — returns structured JSON describing any command:

```bash
basecamp todos --agent --help
```

```json
{"command":"todos","path":"basecamp todos","short":"...","long":"...","usage":"...","notes":["..."],
 "subcommands":[{"name":"sweep","short":"...","path":"basecamp todos sweep"}],
 "flags":[{"name":"assignee","type":"string","default":"","usage":"..."}],
 "inherited_flags":[{"name":"json","shorthand":"j","type":"bool","default":"false","usage":"..."}]}
```

Walk the tree: start at `basecamp --agent --help` for top-level commands, then drill into any subcommand. Commands carry domain-specific agent hints (e.g., "`--assignee` filters the account-wide listing only; within a project, fetch all and filter client-side").

### Pagination

```bash
basecamp <cmd> --limit 50   # Cap results (default varies by resource)
basecamp <cmd> --all        # Fetch all (may be slow for large datasets)
basecamp <cmd> --page 1     # First page only, no auto-pagination
```

`--all` and `--limit` are mutually exclusive. `--page` cannot combine with either.

### Smart Defaults

- `--assignee me` resolves to current user
- `--due tomorrow` / `--due +3` / `--due "next week"` — natural date parsing, **when setting a due date** (`todos create`, `todos update`, `cards create`, and so on)
- `--due` on a **listing** is a different flag and does not take dates: it accepts only `with`, `without`, or `overdue`, and only account-wide. `basecamp todos list --due tomorrow` is rejected. For date-based listing use `--overdue`, `--no-due-date`, or `basecamp assignments due <scope>`
- Project from `.basecamp/config.json` if `--in` not specified
- Multiple identities use named profiles: `basecamp profile create <name>`, then select one with global `--profile <name>` or `BASECAMP_PROFILE=<name>`.

## Quick Reference

> **Note:** Most queries require project scope (via `--in <project>` or `.basecamp/config.json`). Cross-project exceptions: `basecamp reports assigned`, `basecamp assignments`, `basecamp reports overdue`, `basecamp reports schedule`, `basecamp recordings <type>`, `basecamp notifications`, `basecamp gauges list`.
>
> Seven list commands also list account-wide: `basecamp todos list --all-projects --json`, and likewise `cards list`, `messages list`, `comments list`, `files list`, `forwards list`, and `checkins answers`.

| Task | Command |
|------|---------|
| List projects | `basecamp projects list --json` |
| My todos (in project) | `basecamp todos list --assignee me --in <project> --json` |
| My todos (cross-project) | `basecamp reports assigned --json` (defaults to "me") |
| My schedule (cross-project) | `basecamp reports schedule --json` (upcoming events across all projects) |
| All todos (cross-project) | `basecamp todos list --all-projects --json` (grouped by project) |
| Overdue todos (in project) | `basecamp todos list --overdue --in <project> --json` |
| Overdue todos (cross-project) | `basecamp todos list --all-projects --overdue --json` (flat, oldest first) or `basecamp reports overdue --json` (bucketed by lateness) |
| All cards (cross-project) | `basecamp cards list --all-projects --json` (grouped by project) |
| Someone's todos (cross-project) | `basecamp todos list --all-projects --assignee "Ann" --json` (server-side filter) |
| Two people's todos (cross-project) | `basecamp todos list --all-projects --assignee ann --assignee bob --json` (matches either) |
| Someone's cards (cross-project) | `basecamp cards list --all-projects --assignee "Ann" --json` |
| Todos with no due date set (cross-project) | `basecamp todos list --all-projects --due without --json` |
| My bookmarks | `basecamp bookmarks list --json` |
| Bookmark something | `basecamp bookmarks add <id-or-url> --json` |
| Is it bookmarked? | `basecamp bookmarks check <id-or-url> --json` (always exits 0) |
| Bubble a recording up | `basecamp bubble-up add <id-or-url> --json` |
| Schedule a bubble-up | `basecamp bubble-up add <id-or-url> --at tomorrow --json` |
| Pop a bubble-up | `basecamp bubble-up remove <id-or-url> --json` |
| My unpublished drafts | `basecamp drafts list --json` |
| Read my personal note | `basecamp notes show --json` |
| Replace my personal note | `basecamp notes set "<content>" --json` |
| Check-ins I owe answers to | `basecamp checkins reminders --json` |
| Add to Up Next | `basecamp assignments prioritize <id> --json` |
| Recolor a calendar | `basecamp calendars update <id-or-url> --color blue --json` |
| Todo outside any list | `basecamp todos create "<content>" --loose --in <project> --json` |
| Assign todo | `basecamp assign <id> [id...] --to <person> --in <project> --json` |
| Assign card | `basecamp assign <id> [id...] --card --to <person> --in <project> --json` |
| Assign card step | `basecamp assign <id> [id...] --step --to <person> --in <project> --json` |
| Create todo | `basecamp todos create "Task" --in <project> --list <list> --json` |
| Create todolist | `basecamp todolists create "Name" --in <project> --json` |
| Complete todo | `basecamp todos complete <id> --json` |
| List cards | `basecamp cards list --in <project> --json` |
| Create card | `basecamp cards create "Title" --in <project> --json` |
| Complete card | `basecamp cards done <id|url> --in <project> --json` |
| Move card | `basecamp cards move <id> --to <column> [--position N] --in <project> --json` |
| Move card to on-hold | `basecamp cards move <id> --on-hold --in <project> --json` |
| Move card to another project | `basecamp cards move <id> --to-wormhole <wormhole_id> --in <project> --json` (async teleport) |
| Post message | `basecamp messages create "Title" "Body" --in <project> --json` |
| Post with @mention | `basecamp messages create "Title" "Hey @First.Last, ..." --in <project> --json` |
| Post silently | `basecamp messages create "Title" "Body" --no-subscribe --in <project> --json` |
| Post to chat | `basecamp chat post "Message" --in <project> --json` |
| List pings | `basecamp notifications --json --jq '.data.reads[]? | select(.section == "pings")'` |
| Read ping thread | `basecamp api get "/buckets/<circle_id>/chats/<chat_id>/lines.json" --agent` |
| Post to ping thread | `basecamp api post "/buckets/<circle_id>/chats/<chat_id>/lines.json" --data '{"content":"<p>message</p>"}' --json` |
| Add comment | `basecamp comments create <recording_id> "Text" --in <project> --json` |
| Inspect comment / reply atoms | `basecamp comments show <url> --json` → `reply_target` + `mention` in `.data` |
| List attachments | `basecamp attachments list <id\|url> --json` |
| Download attachments | `basecamp attachments download <id> --out /tmp/` |
| Show + download | `basecamp todos show <id> --download-attachments --json` |
| Stream attachment to stdout | `basecamp attachments download <id> --file <name> --out -` |
| Change history for an item | `basecamp events <id\|url> --json` (when a card moved columns, when a todo was completed) |
| Search | `basecamp search "query" --json` |
| Parse URL | `basecamp url parse "<url>" --json` |
| Upload file | `basecamp files uploads create <file> [--vault <folder_id>] --in <project> --json` |
| Download file | `basecamp files download <id> --in <project>` |
| Stream file to stdout | `basecamp files download <id> --out - --in <project>` |
| Download storage URL | `basecamp files download "https://storage.3.basecamp.com/.../download/report.pdf"` |
| My assignments | `basecamp assignments --json` (priorities + non-priorities) |
| Overdue assignments | `basecamp assignments due overdue --json` |
| Completed assignments | `basecamp assignments completed --json` |
| Notifications | `basecamp notifications --json` |
| Mark notification read | `basecamp notifications read <id> --json` |
| All bubble-ups (BC5) | `basecamp notifications bubbleups --json` |
| Gauges (account-wide) | `basecamp gauges list --json` |
| Gauge needles | `basecamp gauges needles --in <project> --json` |
| Create needle | `basecamp gauges create --position 75 --color green --in <project> --json` |
| Account details | `basecamp accounts show --json` |

## URL Parsing

**Parse URLs before acting on them — unless you're handing the URL to a command
that accepts a URL directly** (`show`, `comments show`, `comments thread`,
`attachments list`/`attachments download`), which extract the IDs for you. Only `comments show` and
`comments thread` verify the URL's host and account before any fetch. For other
URL-accepting commands, only pass URLs from a trusted Basecamp host:
`basecamp url parse` extracts IDs but does **not** validate the URL's origin, so
parsing an attacker-controlled path yields trusted-looking IDs.

```bash
basecamp url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598" --json
```

Returns: `account_id`, `project_id`, `type`, `recording_id`, `comment_id` (from fragment).

**URL patterns:**
- `/buckets/27/messages/123` - Message 123 in project 27
- `/buckets/27/messages/123#__recording_456` - Comment 456 on message 123
- `/buckets/27/card_tables/cards/789` - Card 789
- `/buckets/27/card_tables/columns/456` - Column 456 (for creating cards)
- `/buckets/27/todos/101` - Todo 101
- `/buckets/27/uploads/202` - Upload/file 202
- `/buckets/27/documents/303` - Document 303
- `/buckets/27/schedule_entries/404` - Schedule entry 404

**Replying to comments:**
```bash
# Comments are flat - reply to the parent recording_id, not the comment_id
basecamp url parse "https://...messages/123#__recording_456" --json
# Returns recording_id: 123 (parent), comment_id: 456 (fragment) - comment on 123, not 456
basecamp comments create 123 "Reply" --in <project>

# Or get the whole reply-ready context deterministically in one call:
basecamp comments thread "https://...messages/123#__recording_456" --json
# .data.reply_target.recording_id  → where to post the reply
# .data.reply_target.account_id    → the account that reply belongs to (build a fully-qualified command)
# .data.focus.author.mention.syntax → paste-ready [@Name](mention:SGID)
# .data.comments                   → surrounding discussion (default window of 41)
# --all returns every fetched comment; --window N sets the window size
# When the account came from the URL (none configured), the reply breadcrumb carries --account
```

## Decision Trees

### Finding Content

```
Need to find something?
├── Know the type + project? → basecamp <type> list --in <project> --json
│   (some groups have default list behavior; use --agent --help if unsure)
├── My assigned work? → basecamp assignments --json (priorities + non-priorities)
│   Or: basecamp reports assigned --json (traditional view, defaults to "me")
├── My overdue assignments? → basecamp assignments due overdue --json
├── My notifications? → basecamp notifications --json
├── Upcoming schedule? → basecamp reports schedule --json (cross-project)
├── Overdue across projects? → basecamp reports overdue --json
├── Browse by type cross-project? → basecamp recordings <type> --json
│   (types: todos, messages, documents, comments, cards, uploads)
│   Note: Defaults to active status; use --status archived for archived items
│   ⚠ No assignee data — cannot filter by person; use reports assigned instead
├── Full-text search? → basecamp search "query" --json
├── Have a comment URL, or a notification link targeting a comment? → basecamp comments thread <url> --json
└── Have a URL? → basecamp url parse "<url>" --json
```

### Modifying Content

```
Want to change something?
├── Have URL? → basecamp url parse "<url>" → use extracted IDs
├── Have ID? → basecamp <resource> update <id> --field value
├── Change status? → basecamp recordings trash|archive|restore <id>
├── Complete todo? → basecamp todos complete <id>
├── Complete card? → basecamp cards done <id|url> --in <project>
└── Reply to a comment? → basecamp comments show <url> --jq '.data | {reply_target, mention}'
    (one call, cheap atoms — the mention is machine-only, so use --jq/--json, not plain show)
    or basecamp comments thread <url> when you need the surrounding discussion;
    then basecamp comments create <reply_target.recording_id> <text>
```

## Common Workflows

### Link Code to Basecamp Todo

```bash
# Get commit info and comment on todo (use printf %q for safe quoting)
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
basecamp comments create <todo_id> "Commit $COMMIT: $(printf '%s' "$MSG")" --in <project>

# Complete when done
basecamp todos complete <todo_id>
```

### Track PR in Basecamp

```bash
# Create todo for PR work
basecamp todos create "Review PR #42" --in <project> --assignee me --due tomorrow

# When merged
basecamp todos complete <todo_id>
basecamp chat post "Merged PR #42" --in <project>
```

### Bulk Process Overdue Todos

```bash
# Preview overdue todos
basecamp todos sweep --overdue --dry-run --in <project>

# Complete all with comment
basecamp todos sweep --overdue --complete --comment "Cleaning up" --in <project>
```

### Mentioning people (preferred — deterministic)

```bash
# 1. Look up the person
basecamp people pingable --jq '.data[] | select(.name == "Jane Smith")'
# => {"id": 42000, "attachable_sgid": "BAh7CEkiCG...", "name": "Jane Smith"}

# 2. Use SGID in Markdown mention syntax (zero API calls during post)
basecamp comments create 123 "Hey [@Jane Smith](mention:BAh7CEkiCG...), check this" --in <project>

# Or use person ID (one lookup during post)
basecamp comments create 123 "Hey [@Jane Smith](person:42000), check this" --in <project>
```

### Mentioning people (interactive — may be ambiguous)

```bash
# Fuzzy matching: use @First.Last to reduce ambiguity
basecamp comments create <id> "@Jane.Smith, please review this" --in <project>
basecamp messages create "Update" "cc @Jane, @Alex" --in <project>
basecamp chat post "@Jane, done!" --in <project>

# Ambiguous names return an error with suggestions
# Use @First.Last for disambiguation
```

### Move Card Through Workflow

```bash
# List columns to get IDs
basecamp cards columns --in <project> --json

# Complete a card (moves it to the Done column automatically)
basecamp cards done <card_id> --in <project>

# Move card to column
basecamp cards move <card_id> --to <column_id> --in <project>

# Move card to specific position in column (1-indexed)
basecamp cards move <card_id> --to <column_id> --position 1 --in <project>

# Move card to on-hold section of its current column
basecamp cards move <card_id> --on-hold --in <project>

# Move card to on-hold section of a specific column (numeric ID)
basecamp cards move <card_id> --to <column_id> --on-hold --in <project>

# Move card to on-hold section of a named column (requires --card-table)
basecamp cards move <card_id> --to "Column Name" --on-hold --card-table <table_id> --in <project>
```

### Download File from Basecamp

```bash
basecamp files download <upload_id> --in <project> --out ./downloads

# Download attachment from a storage URL (no --in needed)
basecamp files download "https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf"

# Stream to stdout (for piping)
basecamp files download <upload_id> --out - --in <project>
```

### Working with Attachments (Multimodal Agent Workflow)

Messages, todos, cards, and documents may contain images and file attachments
(mockups, screenshots, annotated designs). Show commands surface these as
field-scoped collections — `content_attachments` and/or `description_attachments`
— keyed by which rich-text attribute contained them. The notice field hints at
the download command.

**Step 1: Fetch the recording and check for attachments**
```bash
basecamp todos show <id> --json
# Response includes description_attachments when attachments are present
# Messages/documents use content_attachments; cards may have both
# The notice field hints: "3 attachment(s) — download: basecamp attachments download <id>"
```

**Step 2 (one-shot): Download attachments with the show command**
```bash
# --download-attachments fetches + downloads in one shot
basecamp todos show <id> --download-attachments --json
# content_attachments/description_attachments entries now include "path" pointing to local files
# Downloads to OS temp dir by default, or specify: --download-attachments /tmp/att
```

**Step 2 (two-step alternative): Download separately**
```bash
# Download all at once (shows progress on stderr)
basecamp attachments download <id> --out /tmp/attachments
```

**Step 3: View images with your native file-read tool**
For multimodal LLMs (Claude, Gemini), use your file-read tool on the `path`
from the response to view downloaded images directly — no browser needed.
This surfaces visual context (mockups, screenshots, annotated designs) that
is often the most important part of a Basecamp todo or message.

```bash
# Stream a single image to stdout for piping
basecamp attachments download <id> --file mockup.png --out -

# Select by index when names collide
basecamp attachments download <id> --index 2 --out -
```

**Key pattern:** When a show command response contains `content_attachments`
or `description_attachments`, always download and view them — visual context is
often more important than the text content. Use `--download-attachments` for
one-shot fetch+download, or follow the breadcrumb hint for two-step control.

## Resource Reference

### Projects

```bash
basecamp projects list --json               # List all
basecamp projects show <id> --json          # Show details
basecamp projects create "Name" --json      # Create
basecamp projects update <id> --name "New"  # Update
basecamp projects trash <id>                # Move to trash (recoverable)
```

**Archiving a project:** the CLI does not have a dedicated archive command, but the
underlying status endpoint can be hit via raw API. Same path works for restoring
to active or moving to trashed.

```bash
basecamp api put "projects/<id>/status/archived" -d '{}' --json   # Archive
basecamp api put "projects/<id>/status/active" -d '{}' --json     # Unarchive
basecamp api put "projects/<id>/status/trashed" -d '{}' --json    # Trash (same as `projects trash`)
```

Verify with `basecamp projects show <id> --jq '.data.status'`.

### Todos

```bash
basecamp todos list --in <project> --json               # List in project
basecamp todos list --assignee me --in <project>        # My todos
basecamp todos list --overdue --in <project>            # Overdue only
basecamp todos list --status completed --in <project>   # Completed
basecamp todos list --list <todolist_id> --in <project> # In specific list
basecamp todos create "Task" --in <project> --list <list> --assignee me --due tomorrow
basecamp todos complete <id> [id...]                    # Complete (multiple OK)
basecamp todos uncomplete <id>                          # Reopen
basecamp assign <id> [id...] --to <person> --in <project>       # Assign to-do (multiple OK)
basecamp unassign <id> [id...] --from <person> --in <project>   # Remove to-do assignee (multiple OK)
basecamp assign <id> [id...] --card --to <person> --in <project>   # Assign card
basecamp unassign <id> [id...] --card --from <person> --in <project> # Remove card assignee
basecamp assign <id> [id...] --step --to <person> --in <project>   # Assign card step
basecamp unassign <id> [id...] --step --from <person> --in <project> # Remove step assignee
basecamp todos position <id> --to 1                     # Move to top
basecamp todos position <id> --to 1 --list <id|name|url> # Move to different list
basecamp todos sweep --overdue --complete --comment "Done" --in <project>
basecamp todos create "Task" --in <project> --list <list> --notify-on-completion "Jane,Bob"  # Notify when done
basecamp todos update <id> --notify-on-completion "Jane"  # Set who's notified on completion
basecamp todos update <id> --no-notify-on-completion      # Clear completion notifications
```

**Flags:** `--assignee` (repeatable; server-side account-wide, client-side within a project; also on `cards list` account-wide, but not on messages), `--status` (completed/incomplete/archived/trashed), `--overdue`, `--list`, `--due` (**listing filter: `with`/`without`/`overdue` only, account-wide only** — not a date; see Smart Defaults), `--limit`, `--all`

**Completion subscribers** ("When done, notify…"): set with
`--notify-on-completion <names or IDs, comma-separated>` on `todos create` and
`todos update`; clear with `--no-notify-on-completion` on `todos update`.
Plain updates (title, due date, etc.) preserve existing completion subscribers.

**Todo Subtasks (checklist steps):** Basecamp to-do subtasks are stored as
`Kanban::Step` records, even when their parent is a normal `Todo`. The regular
`basecamp todos show` response may not include them; use
`basecamp recordings list --in <project> --type Kanban::Step` and filter by
`parent.id` to list/check subtasks for a todo.

```bash
# Create a subtask under a todo.
# Use the numeric project ID and todo ID in this card-style path.
basecamp api post /buckets/<project_id>/card_tables/cards/<parent_todo_id>/steps.json \
  --data '{"title":"Subtask title"}' \
  --json

# Read or edit a subtask
basecamp api get /buckets/<project_id>/card_tables/steps/<step_id>.json --json
basecamp api put /buckets/<project_id>/card_tables/steps/<step_id>.json \
  --data '{"title":"Updated subtask title"}' \
  --json

# List subtasks for a todo
PARENT_TODO_ID=<parent_todo_id> \
basecamp recordings list --in <project> --type Kanban::Step --all \
  --jq '.data[] | select(.parent.id==(env.PARENT_TODO_ID | tonumber)) | {id,title,status,parent:.parent.id,url}'

# Assign or set a due date. Send only what you're changing — omitted fields are
# left alone. `assignee_ids` replaces the whole list, so name everyone who stays.
basecamp api put /buckets/<project_id>/card_tables/steps/<step_id>.json \
  --data '{"assignee_ids":[<person_id>,<existing_person_id>],"due_on":"<YYYY-MM-DD>"}' \
  --json

# Complete or reopen a subtask
basecamp api put /buckets/<project_id>/card_tables/steps/<step_id>/completions.json \
  --data '{"completion":"on"}' \
  --json
basecamp api put /buckets/<project_id>/card_tables/steps/<step_id>/completions.json \
  --data '{"completion":"off"}' \
  --json

# Trash a subtask from the todo UI by trashing the step record (Kanban::Step)
basecamp recordings trash <step_id> --in <project> --json
```

Key points: replace numeric placeholders such as `<project_id>`,
`<parent_todo_id>`, and `<person_id>` before running the examples. Bucket-scoped
API paths require a numeric project/bucket ID; `--in <project>` can still accept
a project name where CLI commands support name resolution. For creating todo
subtasks, Basecamp accepts the parent todo ID in the
`/buckets/<project_id>/card_tables/cards/<parent_todo_id>/steps.json` path. To
list subtasks under a todo, use
`basecamp recordings list --in <project> --type Kanban::Step` with the
`parent.id` filter shown above.

Completed subtasks have `completed: true` and a `completion` object with
`created_at` and `creator`. Open subtasks have `completed: false` and no
`completion` object. Trashed subtasks may still be readable directly with
`status: "trashed"` and `inherits_status: false`, but they no longer appear in
the todo UI.

In testing with todo-backed steps, these bucket-scoped direct `GET` requests
returned `not_found`:
`/buckets/<project_id>/card_tables/cards/<parent_todo_id>/steps.json`,
`/buckets/<project_id>/card_tables/cards/<parent_todo_id>.json`, and
`/buckets/<project_id>/todos/<parent_todo_id>/steps.json`. To inspect trashed
subtasks, add `--status trashed`; archived parents may require
`--status archived`.

**Raw step updates are partial.** `PUT .../card_tables/steps/<id>.json` leaves
every parameter you omit unchanged, so send only the fields you are changing.
Echoing back a `title` you did not mean to change is not merely redundant — it
reverts anyone who edited the title between your read and your write. To clear a
value, say so explicitly: `"due_on": null` clears the due date, `"assignee_ids":
[]` removes everyone. `assignee_ids` always replaces the whole list rather than
adding to it, so name every person who should remain assigned.

(This is bc3#12521. Before it, an omitted field *was* cleared and a title-less
update was rejected, which is why older guidance said to resend the title. Todo
subtasks and card steps share one endpoint and one contract — `PUT
card_tables/steps/:id` routes to the same controller for both.)

The generic
`basecamp assign <step_id> --step ...` command is intended for card steps and
may fail with `Bad Request` for todo-backed steps, so prefer `assignee_ids` on
the raw step update endpoint for todo subtasks.

### Todolists

Todolists are containers for todos. Create a todolist before adding todos.

```bash
basecamp todolists list --in <project> --json              # List todolists
basecamp todolists show <id> --in <project>                # Show details
basecamp todolists create "Name" --in <project> --json     # Create
basecamp todolists create "Name" --description "Desc" --in <project>
basecamp todolists create "Name" --visible-to-clients --in <project>  # Visible to clients
basecamp todolists update <id> --name "New" --in <project> # Update
basecamp todolists position <id> --to 1                     # Reorder one list (1 = top)
basecamp todolists position <id> <id> <id>                  # Order incomplete lists, top→bottom
```

Bulk `position` sets the visible order in one command: pass incomplete lists from
the same todoset, top to bottom. It always places them at the top.

### Cards (Kanban)

**Note:** `--assignee` on `cards list` is **account-wide only** — pass `--all-projects` (or have no project in scope) and it becomes a real server-side filter. Within a single project cards have no assignee filter: fetch all and filter client-side. `--due with|without|overdue` is account-wide only on cards too. If a project has multiple card tables, you must specify `--card-table <id>`. When you get an "Ambiguous card table" error, the hint shows available table IDs and names.

```bash
basecamp cards list --in <project> --json             # All cards
basecamp cards list --card-table <id> --in <project>  # Specific table (required if multiple)
basecamp cards list --column <id> --in <project>      # Cards in column
basecamp cards columns --in <project> --json          # List columns (needs --card-table if multiple)
basecamp cards show <id> --in <project>               # Card details
basecamp cards create "Title" "<p>Body</p>" --in <project> --column <id>
basecamp cards update <id> --title "New" --due tomorrow --assignee me
basecamp cards done <id|url> --in <project>           # Move to the Done column automatically
basecamp cards move <id> --to <column_id>             # Move to column (numeric ID)
basecamp cards move <id> --to "Done" --card-table <table_id>  # Move by name (needs table)
basecamp cards move <id> --to "Done" --position 1 --card-table <table_id>  # Move to position
basecamp cards move <id> --on-hold                    # Move to on-hold of current column
basecamp cards move <id> --to <column_id> --on-hold   # Move to on-hold of target column
```

**Cross-project card move (wormholes):** the only way to move a card to another
project is to teleport it through a *wormhole* — a portal on the card table that
sends cards to a preconfigured column on another project's card table (max 4 per
table). The teleport is **asynchronous and mints a new card id**: after the move
is accepted, the server copies the card into the destination and deletes the
original, so the **original id 404s** — do not reuse it.

```bash
basecamp cards wormholes list --in <project>          # Discover wormholes (id, destination, linked)
basecamp cards wormholes create --to-column <id|url> --in <project>   # Link to a column on another table (≤4)
basecamp cards wormholes update <id> --to-column <id|url> --in <project>
basecamp cards wormholes delete <id> --in <project>
basecamp cards move <card_id> --to-wormhole <wormhole_id> --in <project>          # Teleport (async)
basecamp cards move <card_id> --to-wormhole <destination_column_url> --in <project>  # Match by destination column
```

`--to-wormhole` is mutually exclusive with `--to`/`--on-hold`/`--position`. Pass
a numeric wormhole id to route directly, or a destination-column URL to match it
against the source table's wormholes.

**Archived/trashed cards:** `cards list` only returns active cards. For archived or trashed cards, use `basecamp recordings cards --status archived --in <project>` or `--status trashed`.

**Identifying completed cards:** Cards in Done columns have `parent.type: "Kanban::DoneColumn"` and `completed: true`. Use this to identify completed cards that haven't been archived.

**When a card moved columns:** don't read `updated_at` — it changes on any
modification. Use the event history instead: `basecamp events <card_id> --json`
records an `adopted` event for every column move, and a card crossing into or
out of a Done column pairs that with `completed`/`uncompleted`. See
[Events](#events-change-history).

**Card Steps (checklists):**
```bash
basecamp cards steps <card_id> --in <project>     # List steps
basecamp cards step create "Step" --card <id> --in <project>
basecamp cards step complete <step_id> --in <project>
basecamp cards step uncomplete <step_id>
```

**Column management:**
```bash
basecamp cards column show <id> --in <project>
basecamp cards column create "Name" --in <project>
basecamp cards column update <id> --title "New"
basecamp cards column move <id> --position 2
basecamp cards column color <id> --color blue
basecamp cards column on-hold <id>                # Enable on-hold section
basecamp cards column watch <id>                  # Subscribe to column
```

### Messages

```bash
basecamp messages list --in <project> --json  # List messages
basecamp messages show <id> --in <project>    # Show message
basecamp messages create "Title" "Body" --in <project>
basecamp messages create "Draft" "WIP" --draft --in <project>  # Create draft
basecamp messages publish <id>               # Publish a draft
basecamp messages update <id> --title "New" --body "Updated"
basecamp messages pin <id> --in <project>     # Pin to top
basecamp messages unpin <id>                  # Unpin
```

**Archived/trashed messages:** `messages list` only returns active messages. For archived or trashed messages, use `basecamp recordings messages --status archived --in <project>` or `--status trashed`.

**Flags:** `--draft` (create as draft), `--no-subscribe` (silent, no notifications), `--subscribe "people"` (comma-separated names, emails, IDs, or "me"; mutually exclusive with `--no-subscribe`), `--message-board <id>` (if multiple boards), `--visible-to-clients` (make visible to clients on the project; omit for the server default)

```bash
basecamp messages create "Bot update" "Done" --no-subscribe --in <project>
basecamp messages create "FYI" "Note" --subscribe "Alice,bob@x.com" --in <project>
basecamp messages create "For the client" "..." --visible-to-clients --in <project>
```

**Client visibility at create time:** `messages create`, `todolists create`,
`schedule create`, `checkins question create`, and `tools create` accept
`--visible-to-clients` to post a client-visible recording in one call (for
`tools create`, only chat and kanban_board tool types honor it — other types
inherit the project default). Omitting the flag uses the
server default, which is context-dependent: **team-only when you post as a team
member**, but a **client-authenticated caller always creates client-visible
records** (an explicit `--visible-to-clients=false` is overridden server-side for
client callers). Passing `--visible-to-clients` posts client-visible in every
case. To change visibility on an already-created recording, use
`recordings visibility <id> --visible`.

### Comments

```bash
basecamp comments list <recording_id> --in <project> --json
basecamp comments show <comment-id|comment-url> --json            # Now returns reply_target + paste-ready mention (JSON)
basecamp comments thread <comment-id|comment-url> --json          # Reply-ready: parent + focus + discussion + @mention
basecamp comments thread <comment-id> --all --json                # Every fetched comment instead of a window
basecamp comments thread <comment-id> --window 11 --json          # Focus-centered window of 11
basecamp comments create <recording_id> "Text" --in <project>
basecamp comments create <recording_id> "@Jane.Smith, looks good!" --in <project>  # With @mention
basecamp comments update <id> "Updated" --in <project>
```

**Cheap atoms vs. deep context (choose by need):**
- `comments show <url> --jq '.data | {reply_target, mention}'` — one API call. Returns
  `reply_target` (`recording_id` — where a reply is posted, comments are flat — plus
  `account_id`) and a paste-ready author `mention` (JSON only; human output shows a reply
  breadcrumb). Use this for the exact-comment reply atoms.
- `comments thread <url>` — two extra calls. Adds the full parent recording, the
  surrounding discussion (windowed, truncation-honest), and focus attachments. Use this
  when the surrounding discussion matters.

### Files & Documents

```bash
basecamp files list --in <project> --json               # List all (folders, files, docs)
basecamp files list --vault <folder_id> --in <project>  # List folder contents
basecamp files list --all-projects --json               # Across every project (first 100)
basecamp files list --all-projects --limit 500          # Walk pages until 500 collected
basecamp files list --all-projects --page 2             # Exactly page 2
basecamp files list --all-projects --all                # Every page (slow on big accounts)
basecamp files show <id> --in <project>                 # Show item (auto-detects type)
basecamp files versions <upload_id> --json              # Every version of an uploaded file
basecamp files versions <upload_id> --limit 5 --json    # Cap results (default: all)
basecamp files replace <upload_id> <file>               # Replace the file, keep the ID/URL/comments
basecamp files replace <upload_id> <file> --description "v2 notes"  # Also set a new description
basecamp files download <id> --in <project>             # Download file
basecamp files download <id> --out ./dir                # Download to specific dir
basecamp files download "https://storage.../download/f" # Download from storage URL
basecamp files uploads create <file> --in <project>      # Upload file to root
basecamp files uploads create <file> --vault <folder_id> --in <project>  # Upload to folder
basecamp files uploads create <file> --visible-to-clients --in <project>  # Client-visible (root folder only)
basecamp files folder create "Folder" --in <project>
basecamp files doc create "Doc" "Body" --in <project>
basecamp files doc create "Draft" --draft --in <project>
basecamp files doc create "Notes" "..." --no-subscribe --in <project>
basecamp files doc create "For client" "..." --visible-to-clients --in <project>  # Client-visible (root folder only)
basecamp files update <document_id> --title "New" --content "Updated"
basecamp files update <document_id> --title "New" --in <project>      # Preserves existing document content
basecamp files update <document_id> --content "Updated" --in <project> # Preserves existing document title
```

**Document update semantics:** `basecamp files update <document_id>` is safe for partial updates in the CLI: when you pass only `--title` or only `--content`, the CLI first fetches the current document and preserves the untouched field.

**Client visibility at create time:** `doc create` and `uploads create` accept
`--visible-to-clients`, but the server only honors it in the project's **root
Docs & Files folder**. Targeting a nested folder (`--vault`/`--folder`) with the
flag is a hard error raised before anything is uploaded — a nested item inherits
its folder's visibility, and that can't be changed per-item afterward (the
visibility endpoint rejects nested docs/uploads). To make a nested item
client-visible, create it in the root folder, or change the eligible top-level
ancestor that controls the folder's visibility first. Omitting the flag uses the
server default; as with Messages, a **client-authenticated caller always creates
client-visible records** regardless. `recordings visibility` is **not** a
remediation for nested docs/uploads.

**Upload versions:** replacing a file keeps the earlier copies under the same
upload ID, so `basecamp files versions <upload_id>` is how you see the history of
one file. A file that was never replaced returns its single current version, not
an error. Only `--page 1` is accepted; use `--all` to walk every page.
`basecamp files replace <upload_id> <file>` publishes a new version in place —
the upload keeps its ID, URL and comments, nobody is notified, and the
description carries forward unless `--description` is given. Use it instead of
`uploads create` when shipping a new build of the same file.

**Subcommands:** `folders`, `uploads`, `documents` (each with pagination flags)

### Schedule

For upcoming events across all projects, use `basecamp reports schedule --json`.

```bash
basecamp schedule info --in <project> --json       # Schedule info
basecamp schedule entries --in <project> --json   # List entries
basecamp schedule show <id> --in <project>        # Entry details
basecamp schedule show <id> --date 20240315       # Specific occurrence (recurring)
basecamp schedule create "Event" --starts-at "2024-03-15T09:00:00Z" --ends-at "2024-03-15T10:00:00Z" --in <project>
basecamp schedule create "Meeting" --all-day --notify --participants 1,2,3 --in <project>
basecamp schedule create "Sync" --starts-at "..." --ends-at "..." --no-subscribe --in <project>
basecamp schedule update <id> --summary "New title" --starts-at "..."
basecamp schedule settings --include-due --in <project>  # Include todos/cards due dates
```

**Flags:** `--all-day`, `--notify`, `--participants <ids>`, `--no-subscribe`, `--subscribe "people"` (mutually exclusive), `--status` (active/archived/trashed), `--visible-to-clients` (make visible to clients; omit for the server default)

### Check-ins

```bash
basecamp checkins --in <project> --json           # Questionnaire info
basecamp checkins questions --in <project>        # List questions
basecamp checkins question <id> --in <project>    # Question details
basecamp checkins answers <question_id> --in <project>  # List answers
basecamp checkins answers <question_id> --by me --in <project>  # My answers only
basecamp checkins answers <question_id> --by "Alice Smith" --in <project>  # Filter by person (name, email, or ID)
basecamp checkins answer <id> --in <project>      # Answer details
basecamp checkins question create "What did you work on?" --in <project>
basecamp checkins question update <id> "New question" --frequency every_week
basecamp checkins answer create <question-id> "My answer" --in <project>  # Defaults to today
basecamp checkins answer update <id> "Updated" --in <project>
```

**Schedule options:** `--frequency` (every_day, every_week, every_other_week, every_month, on_certain_days), `--days 1,2,3,4,5` (0=Sun), `--time "5:00pm"`

**Client visibility:** `checkins question create` accepts `--visible-to-clients` to make the question visible to clients (omit for the server default; see the note under Messages for the context-dependent rule).

**Managing a question:**

```bash
basecamp checkins question pause <id> --json      # Stop asking it
basecamp checkins question resume <id> --json     # Start asking it again
basecamp checkins question answerers <id> --json  # Who answers it
basecamp checkins question notify <id> --on-answer --json
basecamp checkins question notify <id> --no-on-answer --json
basecamp checkins question notify <id> --digest-include-unanswered --json
```

`notify` changes **your own** settings, and each one is left alone unless you
name it — so `--on-answer` does not silently reset the digest setting. The
`--no-...` spellings send an explicit false; passing neither setting is refused
rather than sent as an empty update.

**Your pending reminders** (account-wide, no `--in`):

```bash
basecamp checkins reminders --json
basecamp checkins reminders --limit 10 --json
```

`reminders` and `answerers` take `--limit` but deliberately **no `--page`**: the
API does not honor a page number on these, so the flag would accept a value it
could not act on.

### Timeline

```bash
basecamp timeline --json                          # Account-wide activity
basecamp timeline --in <project> --json           # Project activity
basecamp timeline me --json                       # Your activity
basecamp timeline --person <id> --json            # Person's activity
```

Use `--limit N` to cap results or `--all` to fetch everything (default: 100 events).

### Events (change history)

`basecamp timeline` reports activity across a project or account. For the audit
trail of one specific item — todo, card, message, document — use `basecamp
events`:

```bash
basecamp events <id|url> --json                   # Change history for one item
basecamp events <id> --limit 25 --json            # Cap results (default 100)
basecamp events <id> --all --json                 # Fetch everything
```

Common `action` values: `created`, `completed`/`uncompleted`,
`assignment_changed`, `content_changed`, `archived`/`unarchived`,
`commented_on`, and — for cards — `adopted`, which is recorded every time a card
moves to another column. That makes `events` the way to answer "when did this
card move?" or "when was this actually finished?", neither of which `updated_at`
can tell you.

`--page` accepts only `1`; use `--all` to walk every page.

### Recordings (Cross-project)

Use `basecamp recordings <type>` for cross-project type browsing. **For assigned todos, prefer `basecamp reports assigned`** — recordings do not include assignee data and cannot be filtered by person.

```bash
basecamp recordings todos --json                  # All todos across projects
basecamp recordings todos --all --json            # All todos (paginate through all)
basecamp recordings messages --in <project>       # Messages in project
basecamp recordings documents --status archived   # Archived docs
basecamp recordings cards --sort created_at --direction asc
basecamp recordings cards --status archived --all --json  # Include archived cards
```

**Types:** `todos`, `messages`, `documents`, `comments`, `cards`, `uploads`

**Status filtering:** By default, only `active` recordings are returned. Use `--status archived` or `--status trashed` to query other statuses. You may need separate queries to get complete data (e.g., active + archived).

**Status management:**
```bash
basecamp recordings trash <id> --in <project>     # Move to trash
basecamp recordings archive <id> --in <project>   # Archive
basecamp recordings restore <id> --in <project>   # Restore to active
basecamp recordings visibility <id> --visible --in <project>  # Show to clients
basecamp recordings visibility <id> --hidden      # Hide from clients
```

### Templates

```bash
basecamp templates list --json                    # List project templates
basecamp templates show <id> --json               # Project template details
basecamp templates create "Template Name"         # Create empty project template
basecamp templates update <id> --name "New Name"
basecamp templates delete <id>                    # Trash project template
basecamp templates construct <id> --name "New Project"  # Create project (async)
basecamp templates construction <template_id> <construction_id>  # Check project status

basecamp templates library --json                 # List active to-do list templates
basecamp templates copy <template_id> --in <project>  # Start copying into To-dos
basecamp templates copy-status <copy_id>          # Check copy status
```

**Asynchronous results:** `construct` returns a construction ID; poll `construction`
until `status="completed"` to get the project. `copy` returns a copy ID; poll
`copy-status` through `pending` and `processing` until it is `completed` or `failed`.

A copy can report the people who need access to the destination project. Show those
people to the user and rerun with `--confirm-adding-people` only after the user
explicitly approves granting that access. Never add this flag automatically.

### Webhooks

```bash
basecamp webhooks list --in <project> --json  # List webhooks
basecamp webhooks show <id> --in <project>    # Webhook details
basecamp webhooks create "https://..." --in <project>
basecamp webhooks create "https://..." --types "Todo,Comment" --in <project>
basecamp webhooks update <id> --active --in <project>
basecamp webhooks update <id> --inactive      # Disable
basecamp webhooks delete <id> --in <project>
```

**Event types:** Todo, Todolist, Message, Comment, Document, Upload, Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer

### Subscriptions

```bash
basecamp subscriptions <recording_id>              # Who's subscribed
basecamp subscriptions subscribe <id>              # Subscribe yourself
basecamp subscriptions unsubscribe <id>            # Unsubscribe
basecamp subscriptions add <id> --people 1,2,3     # Add people
basecamp subscriptions remove <id> --people 1,2,3  # Remove people
```

### Lineup (Account-wide Markers)

```bash
basecamp lineup list                              # List all markers
basecamp lineup create "Milestone" "2024-03-15"   # Create marker
basecamp lineup create "Launch" tomorrow          # Natural date parsing
basecamp lineup update <id> "New Name" "+7"
basecamp lineup delete <id>
```

**Note:** Lineup markers are account-wide, not project-scoped.

### Gauges

Gauges track project progress with colored needles on a 0-100 scale.

```bash
basecamp gauges list --json                           # All gauges (account-wide)
basecamp gauges needles --in <project> --json         # Needles for a project
basecamp gauges needle <id> --json                    # Needle details
basecamp gauges create --position 75 --color green --in <project>
basecamp gauges create --position 50 --color yellow --description "Halfway" --in <project>
basecamp gauges create --position 25 --notify custom --subscriptions 1,2 --in <project>
basecamp gauges update <id> --description "Updated"
basecamp gauges delete <id>
basecamp gauges enable --in <project>                 # Enable gauge on project
basecamp gauges disable --in <project>                # Disable gauge
```

**Colors:** green, yellow, red. **Notify:** everyone, working_on, custom (with `--subscriptions`).

### Assignments

View your assignments across all projects. Separate from `reports assigned` — provides structured priority grouping and due-date scoping.

```bash
basecamp assignments --json                           # All (priorities + non-priorities)
basecamp assignments list --json                      # Same as bare
basecamp assignments completed --json                 # Completed assignments
basecamp assignments due overdue --json               # Overdue
basecamp assignments due due_today --json             # Due today
basecamp assignments due due_tomorrow --json          # Due tomorrow
basecamp assignments due due_later_this_week --json   # Due later this week
```

**Scopes:** overdue, due_today, due_tomorrow, due_later_this_week, due_next_week, due_later.

**Cross-project assignee filtering:** `basecamp todos list --all-projects
--assignee <person>` and `basecamp cards list --all-projects --assignee <person>`
filter server-side across every project. Both are repeatable and match a task
assigned to **any** of the named people. Assignees on nested steps are not
considered, so a card whose step is assigned to someone does not match on that
basis.

**Always pass `--all-projects` when you mean every project.** Without it these
listings are account-wide *only* when no project is in scope — and a configured
default project counts as in scope. With one configured, `--assignee` silently
degrades to a client-side filter over that single project, and `--due` is
rejected outright as account-wide-only. `--all-projects` is what overrides a
configured default, so a recipe that omits it returns different results
depending on the reader's config.

Within a project `--assignee` still works on todos, but there is no server-side
filter, so it fetches everything and narrows client-side. Cards have no
project-scoped `--assignee` at all. `--due with|without|overdue` is account-wide
only on both, and conflicts with `--overdue` and `--no-due-date`, which select
their own listings on the same axis. `--assignee` with `--unassigned` is refused
— the server makes that combination necessarily empty.

**Up Next** — reorder the priority list:

```bash
basecamp assignments prioritize <id> --json      # Add to Up Next
basecamp assignments deprioritize <id> --json    # Remove from Up Next
basecamp assignments reorder <id> --position 1 --json
```

**Which id to pass — three cases, not two.** A to-do or a card is addressed by
the entry's own `id`. A step that is *not yet* prioritized is addressed by the
step's own `id`, found in the parent card's `children`. But once a step *is*
prioritized, the listing shows it under its parent card, so the entry's top-level
`id` belongs to the **card**, and only `priority_recording_id` addresses the
step.

`basecamp assignments list` is the only place `priority_recording_id` appears —
it is in no URL. Read it from there rather than guessing: `deprioritize` targets
one exact recording and the server answers 204 either way, so a wrong id reports
success while changing nothing. If two steps on one card are prioritized, the
listing shows the card once with a single `priority_recording_id` and the
siblings are not separately addressable.

### Personal (bookmarks, bubble-up, drafts, notes)

Private to you, spanning every project — no `--in <project>`.

```bash
basecamp bookmarks list --json
basecamp bookmarks add <id-or-url> --json
basecamp bookmarks remove <id-or-url> --json
basecamp bookmarks check <id-or-url> --json
basecamp bubble-up add <id-or-url> --json
basecamp bubble-up add <id-or-url> --at tomorrow --json
basecamp bubble-up remove <id-or-url> --json
basecamp drafts list --json
basecamp notes show --json
basecamp notes set "<content>" --json
```

`bubble-up add`/`remove` resurface a recording in your readings (the BC5
successor to "save"), addressed by id or pasted URL. `add` bubbles up now by
default; `--at` schedules it — a keyword (`today`, `tomorrow`, `weekend`,
`next_week`) or a calendar date (`YYYY-MM-DD`). Both verbs are idempotent. There is no
per-recording status read (that GET is an unrenderable API gap); the full list
is `basecamp notifications bubbleups`.

`bookmarks add` and `remove` are idempotent — re-adding returns the existing
bookmark, removing an absent one still succeeds. `check` reports
`{"bookmarked": true|false}` and **always exits 0**: both answers are successes,
so a nonzero exit here means the request failed, not that the answer was false.

`bookmarks list` and `drafts list` are bounded like the account-wide listings:
default 100, `--limit N`, `--page N`, `--all` for every page. Drafts are capped
at 250 server-side.

`notes` is a single private scratchpad — one per person, no id, nothing to list.
Before your first write it renders empty rather than 404ing. `set` **replaces**
the whole note (it does not append) and takes content from an argument or
`--file` — either accepts `-` to read stdin (`cat notes.md | basecamp notes set -`);
a pipe without `-` is not consumed. Markdown is converted to HTML.

### Calendars

```bash
basecamp calendars show <id-or-url> --json
basecamp calendars update <id-or-url> --color blue --json
```

**There is no `calendars list`** — the API has no index endpoint, so address a
calendar by id or by pasting its URL. Colors: white, red, orange, yellow, green,
blue, aqua, purple, gray, pink, brown.

### Notifications

```bash
basecamp notifications --json                         # List (page 1)
basecamp notifications list --page 2 --json           # Page 2
basecamp notifications read <id> --json               # Mark as read
basecamp notifications read <id> <id> --page 2 --json # Mark from page 2
basecamp notifications bubbleups --json               # All bubble-ups (BC5)
basecamp notifications list --limit-bubble-ups --json # Cap inline bubble-ups at 2
```

**Note:** `read` resolves notification IDs from the specified page. Use `--page` to match the page you listed.

**Bubble Ups (BC5):** `bubbleups` lists all current and scheduled bubble-ups
(paginated; `--page` fetches a single page). `list --limit-bubble-ups` keeps the
notification feed compact: at most 2 inline bubble-ups, scheduled ones omitted,
with the uncapped counts still reported.

### Accounts

```bash
basecamp accounts list --json                         # List authorized accounts
basecamp accounts use <id>                            # Set default account
basecamp accounts show --json                         # Account details, limits, subscription
basecamp accounts update --name "New Name" --json     # Rename account
basecamp accounts logo upload <file> --json           # Upload logo (PNG/JPEG/GIF/WebP/AVIF/HEIC, 5MB max)
basecamp accounts logo remove --json                  # Remove logo
```

### Chat

```bash
basecamp chat --in <project> --json           # List chats
basecamp chat messages --in <project> --json  # List messages
basecamp chat post "Hello!" --in <project>
basecamp chat post "@Jane.Smith, check this" --in <project>  # With @mention (auto text/html)
basecamp chat line <line_id> --in <project>   # Show line
basecamp chat update <line_id> "edited content" --in <project>  # Edit existing message in place
basecamp chat delete <line_id> --in <project> --force # Delete line (permanent, not trashable; --force required)
```

### Pings (Direct Messages)

Pings are Basecamp's 1-on-1 and small-group direct messages. They are stored as chat transcripts in `Circle` buckets and use the same line API shape as Campfires.

Use `notifications` to discover active ping threads, then use the generic `api` command to read or post lines.

```bash
# Find ping threads visible in notifications.
# circle_id is the Circle bucket ID from the UI URL; chat_id identifies the Chat::Transcript.
basecamp notifications --json \
  --jq '.data.reads[]? | select(.section == "pings") | {bucket_name, app_url, circle_id: (.subscription_url | capture("/buckets/(?<id>[0-9]+)/").id), chat_id: (.subscription_url | capture("/recordings/(?<id>[0-9]+)/").id)}'

# Read a ping thread. Lines are returned newest first.
basecamp api get "/buckets/<circle_id>/chats/<chat_id>/lines.json" --agent

# Post a ping line.
basecamp api post "/buckets/<circle_id>/chats/<chat_id>/lines.json" \
  --data '{"content":"<p>Hey, quick question.</p>"}' --json
```

Ping line records include `creator.name`, `created_at`, `content` HTML, `type`, `bucket.type: "Circle"`, and attachment fields when files or voice notes are present.

Ping URLs use `/circles/<circle_id>` and may include a line anchor after `@`:

```bash
echo "https://app.basecamp.com/<account_id>/circles/44024535@9927050443" \
  | sed -E 's|.*/circles/([0-9]+)(@([0-9]+))?.*|circle:\1 line:\3|'
# circle:44024535 line:9927050443
```

Pings are not returned by `basecamp recordings <type>`. Use `notifications` for discovery and the chat lines API for the conversation.

### People

```bash
basecamp people list --json                          # All people in account
basecamp people list --project <project> --json    # People on project
basecamp me --json                                 # Current user
basecamp people show <id> --json                   # Person details
basecamp people show me --json                     # Your own profile
basecamp people update me --bio "..." --title "..." --json   # Edit your own profile
basecamp people out-of-office me --json            # Your out-of-office status
basecamp people out-of-office me --start 2026-09-14 --end 2026-09-18 --json  # Set out-of-office
basecamp people out-of-office me --clear --json    # Clear out-of-office
basecamp people add <id> --project <project>       # Add to project
basecamp people remove <id> --project <project>    # Remove from project
```

`people update me` edits your own profile (bio, title, name, email, location,
time zone); pass a flag with an empty value to clear that field. `people
out-of-office me` shows your away status, sets it with `--start`/`--end`
(natural language or YYYY-MM-DD, end not before start), or clears it with
`--clear`.

### Search

```bash
basecamp search "query" --json                    # Full-text search (capped at 20; --all for every match)
basecamp search "query" --sort recency --limit 20
basecamp search "query" --project Marketing       # Scope to one project (--in also works)
basecamp search "query" --type todo               # Filter by type: todo, message, document, comment,
                                                  #   card, file, ping, chat, check-in, event, folder,
                                                  #   forward, client
basecamp search "query" --creator me              # Filter by creator (name, email, ID, or 'me')
basecamp search "query" --since last_30_days      # last_7_days|last_30_days|last_90_days|last_12_months|forever
basecamp search "query" --file-type pdf           # Filter attachments: image, audio, video, pdf
basecamp search "query" --exclude-chat            # Drop chat/campfire results
basecamp search metadata --json                   # Recording and file types the API accepts as filters
```

### Generic Show

```bash
basecamp show <type> <id> --in <project> --json                   # Show any recording type (includes up to 100 comments by default)
basecamp show <type> <id> --all-comments --in <project> --json   # Fetch the full discussion when you need every comment
basecamp show <type> <id> --no-comments --in <project> --json    # Skip the extra comments fetch
# Types: todo, todolist, message, comment, card, card-table, document (or omit <type> for generic lookup)

# Typed show commands also support --comments / --all-comments / --no-comments:
basecamp todos show <id> --comments --json                        # Opt in to comments on typed show
basecamp cards show <id> --all-comments --json                    # Fetch all comments on card
basecamp messages show <id> --no-comments --json                  # Suppress comments
# All commentable show commands: todos, messages, cards, files, todolists, schedule, checkins, forwards, chat
```

## Configuration

The CLI uses two directory namespaces: `basecamp` for your Basecamp identity and project relationships, `basecamp` for tool-specific operational data.

```
~/.config/basecamp/           # Basecamp identity (DO NOT read credentials)
├── credentials.json          #   OAuth tokens — NEVER read or log
├── client.json               #   Obsolete (former dev-only client registration; safe to delete)
└── config.json               #   Global preferences (account_id, base_url, format)

~/.cache/basecamp/            # Tool cache (ephemeral, auto-managed)
├── completion.json           #   Tab completion cache
└── resilience/               #   Circuit breaker state

.basecamp/                    # Per-repo config (committed to git)
└── config.json               #   Project defaults (project_id, account_id, todolist_id)
```

**Per-repo config:** `.basecamp/config.json`
```json
{
  "project_id": "12345",
  "todolist_id": "67890"
}
```

**Initialize:**
```bash
basecamp config init
basecamp config set project_id <id>
basecamp config set todolist_id <id>
```

**Config Trust:**

Authority keys (`base_url`, `default_profile`, `profiles`) in local/repo configs are blocked until explicitly trusted. This prevents a cloned repo's config from redirecting OAuth tokens.

```bash
basecamp config trust                    # Trust nearest .basecamp/config.json
basecamp config trust /path/to/.basecamp/config.json  # Trust specific config file
basecamp config trust --list             # Show all trusted configs
basecamp config untrust                  # Revoke trust for nearest config
basecamp config untrust /path/to/.basecamp/config.json  # Revoke trust for specific path
```

**Check context:**
```bash
cat .basecamp/config.json 2>/dev/null || echo "No project configured"
```

**Global config:** `~/.config/basecamp/config.json` (account_id, base_url, format preferences)

## Error Handling

**General diagnostics:**
```bash
basecamp doctor --json                            # Check CLI health, auth, connectivity
```

**Coding agent setup (non-interactive):**
```bash
basecamp setup agents                             # Install skill + connect detected agent(s)
basecamp setup agents --json                      # Structured result envelope
```
`setup agents` installs the baseline skill and connects coding agents without
prompting. Selection is driven by `BASECAMP_SETUP_AGENT` (`claude`, `codex`,
`all`, or `none`); unset auto-detects — one detected agent is connected, several
leave the skill only and surface the per-agent `basecamp setup <id>` commands.

**Rate limiting (429):** The CLI handles backoff automatically. If you see 429 errors, reduce request frequency.

**Authentication errors:**
```bash
basecamp auth status                              # Check auth
basecamp auth login                               # Re-authenticate
basecamp auth login --scope full                  # Full access (the default; ignored by Launchpad)
basecamp auth login --scope read                  # Read-only access (ignored by Launchpad)
basecamp auth login --device-code                 # Headless authentication with manual browser instructions
```

**Network errors / localhost URLs:**
```bash
# Check for dev config
cat ~/.config/basecamp/config.json
# Should only contain: {"account_id": "<id>"}
# Remove base_url/api_url if pointing to localhost
```

**Not found errors:**
```bash
basecamp auth status                              # Verify auth working
cat ~/.config/basecamp/accounts.json              # Check available accounts
```

**Required arguments are positional (not flags):**
- `basecamp todos create "Buy milk"` (not `--content`)
- `basecamp cards create "New feature"` (not `--title`)
- `basecamp messages create "Subject" "Body"` (not `--subject`)
- `basecamp chat post "Hello"` (not `--content`)
- `basecamp comments create <id> "Text"` (not a flag)
- `basecamp webhooks create "https://..." --in <project>` (not `--url`)
- `basecamp checkins answer create <question-id> "content"` (not `--question`)
- `--date YYYY-MM-DD` is optional for `checkins answer create`; if omitted, it defaults to today

**Missing argument errors (code: "usage"):**
When a required positional argument is missing, the CLI returns a structured error naming
the specific argument. Use this for elicitation:

```bash
$ basecamp todos create --json
{"ok": false, "error": "<content> required", "code": "usage", "retryable": false,
 "hint": "Usage: basecamp todos create <content>"}

$ basecamp comments create 123 --json
{"ok": false, "error": "<content> required", "code": "usage", "retryable": false, ...}
```

The `error` field names the missing `<arg>` — use it to prompt the user for the specific value.

**Retryable errors (`retryable`):** every error envelope carries a boolean `retryable` —
`true` when the CLI classified the failure transient (network, timeout, rate limit,
circuit open, most 5xx/gateway responses — not all: 507 and some 500s are verdicts) and
a retry can change the outcome, `false` for a verdict (usage, not found, auth, forbidden,
validation, account limit) and for any error nothing classified. Key on it rather than
on `code` or `error` when deciding whether to retry — `false` means no known reason a
retry would help, not a guarantee of permanence; it is never present on a success
envelope.

**URL malformed (curl exit 3):** Special characters in content. Use plain text or properly escaped HTML.

## Built-in jq Filtering

The CLI has a built-in `--jq` flag powered by gojq — no external `jq` binary required. **Always prefer `--jq` over piping to external `jq`.**

```bash
# Extract fields from data array
basecamp todos list --in <project> --jq '.data[] | select(.completed == false) | .title'
basecamp todos list --in <project> --jq '.data | length'
basecamp todos list --in <project> --jq '[.data[] | {id, title, status}]'

# Access envelope metadata
basecamp todos list --in <project> --jq '.breadcrumbs[0].cmd'
basecamp todos list --in <project> --jq '.meta.stats.requests'

# Filter and transform
basecamp cards list --in <project> --jq '[.data[] | select(.completed == true) | .title]'
basecamp people list --jq '[.data[] | {name: .name, email: .email_address}]'
```

`--jq` implies `--json` — no need to pass both. String results print as plain text; objects and arrays print as formatted JSON.

## Exit Codes

| Exit | Meaning | Fix |
|------|---------|-----|
| 0 | OK | — |
| 1 | Usage error | Check `basecamp <cmd> --help` |
| 2 | Not found | Verify ID/URL exists |
| 3 | Auth error | `basecamp auth login` |
| 4 | Forbidden | Check account/project permissions |
| 5 | Rate limit | Wait and retry (resilience layer handles Retry-After automatically) |
| 6 | Network error | Check connectivity, `basecamp doctor` |
| 7 | API error | Retry; if persistent, check `basecamp doctor` |
| 8 | Ambiguous | Be more specific (use ID instead of name) |

## Learn More

- API concepts: https://github.com/basecamp/bc3-api#key-concepts
- CLI repo: https://github.com/basecamp/basecamp-cli
- API coverage: See API-COVERAGE.md in the CLI repo
