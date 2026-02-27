---
name: basecamp
description: |
  Interact with Basecamp via the Basecamp CLI. Full API coverage: projects, todos, cards,
  messages, files, schedule, check-ins, timeline, recordings, templates, webhooks,
  subscriptions, lineup, and campfire. Use for ANY Basecamp question or action.
triggers:
  # Direct invocations
  - basecamp
  - /basecamp
  # Resource actions
  - basecamp todo
  - basecamp project
  - basecamp card
  - basecamp campfire
  - basecamp message
  - basecamp file
  - basecamp document
  - basecamp schedule
  - basecamp checkin
  - basecamp check-in
  - basecamp timeline
  - basecamp template
  - basecamp webhook
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
  - my basecamp
  - assigned to me
  - overdue todos
  # URLs
  - 3.basecamp.com
  - basecampapi.com
  - https://3.basecamp.com/
invocable: true
argument-hint: "[action] [args...]"
---

# /basecamp - Basecamp Workflow Command

Full CLI coverage: 130 endpoints across todos, cards, messages, files, schedule, check-ins, timeline, recordings, templates, webhooks, subscriptions, lineup, and campfire.

## Agent Invariants

**MUST follow these rules:**

1. **Always use `--json`** for structured, predictable output
2. **Parse URLs first** with `basecamp url parse "<url>"` to extract IDs
3. **Comments are flat** - reply to parent recording, not to comments
4. **Check context** via `.basecamp/config.json` before assuming project
5. **Rich text fields accept Markdown** - the CLI converts to HTML automatically
6. **Project scope is mandatory** — via `--in <project>` or `.basecamp/config.json`. There is no cross-project query mode. For cross-project data, use `basecamp recordings <type>` or loop through projects individually.

### Output Modes

```bash
basecamp <cmd> --json      # JSON envelope with data, summary, breadcrumbs (recommended)
basecamp <cmd> --quiet     # Raw JSON data only, no envelope
basecamp <cmd> --agent     # Machine-readable, no interactive prompts
basecamp <cmd> --ids-only  # Just IDs, one per line
basecamp <cmd> --count     # Just the count
basecamp <cmd> --stats     # Include session stats in output
```

### Pagination

```bash
basecamp <cmd> --limit 50   # Cap results (default varies by resource)
basecamp <cmd> --all        # Fetch all (may be slow for large datasets)
basecamp <cmd> --page 1     # First page only, no auto-pagination
```

`--all` and `--limit` are mutually exclusive. `--page` cannot combine with either.

### Smart Defaults

- `--assignee me` resolves to current user
- `--due tomorrow` / `--due +3` / `--due "next week"` - natural date parsing
- Project from `.basecamp/config.json` if `--in` not specified

## Quick Reference

> **Note:** Most queries require project scope (via `--in <project>` or `.basecamp/config.json`). For cross-project data, use `basecamp recordings <type>` or loop through projects individually.

| Task | Command |
|------|---------|
| List projects | `basecamp projects --json` |
| My todos (in project) | `basecamp todos --assignee me --in <project> --json` |
| All todos (cross-project) | `basecamp recordings todos --json` (filter client-side) |
| Overdue todos | `basecamp todos --overdue --in <project> --json` |
| Create todo | `basecamp todo --content "Task" --in <project> --list <list> --json` |
| Create todolist | `basecamp todolists create --name "Name" --in <project> --json` |
| Complete todo | `basecamp done <id> --json` |
| List cards | `basecamp cards --in <project> --json` |
| Create card | `basecamp card --title "Title" --in <project> --json` |
| Move card | `basecamp cards move <id> --to <column> --in <project> --json` |
| Post message | `basecamp message --subject "Title" --content "Body" --in <project> --json` |
| Post to campfire | `basecamp campfire post --content "Message" --in <project> --json` |
| Add comment | `basecamp comment --content "Text" --on <recording_id> --in <project> --json` |
| Search | `basecamp search "query" --json` |
| Parse URL | `basecamp url parse "<url>" --json` |
| Download file | `basecamp files download <id> --in <project>` |
| Watch timeline | `basecamp timeline --watch` |

## URL Parsing

**Always parse URLs before acting on them:**

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
basecamp comment --content "Reply" --on 123 --in <project>
```

## Decision Trees

### Finding Content

```
Need to find something?
├── Know the type + project? → basecamp <type> --in <project> --json
├── Need cross-project data? → basecamp recordings <type> --json (ONLY cross-project option)
│   (types: todos, messages, documents, comments, cards, uploads)
│   Note: Defaults to active status; use --status archived for archived items
├── Full-text search? → basecamp search "query" --json
└── Have a URL? → basecamp url parse "<url>" --json
```

### Modifying Content

```
Want to change something?
├── Have URL? → basecamp url parse "<url>" → use extracted IDs
├── Have ID? → basecamp <resource> update <id> --field value
├── Change status? → basecamp recordings trash|archive|restore <id>
└── Complete todo? → basecamp done <id>
```

## Common Workflows

### Link Code to Basecamp Todo

```bash
# Get commit info and comment on todo (use printf %q for safe quoting)
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
basecamp comment --content "Commit $COMMIT: $(printf '%s' "$MSG")" --on <todo_id> --in <project>

# Complete when done
basecamp done <todo_id>
```

### Track PR in Basecamp

```bash
# Create todo for PR work
basecamp todo --content "Review PR #42" --in <project> --assignee me --due tomorrow

# When merged
basecamp done <todo_id>
basecamp campfire post --content "Merged PR #42" --in <project>
```

### Bulk Process Overdue Todos

```bash
# Preview overdue todos
basecamp todos sweep --overdue --dry-run --in <project>

# Complete all with comment
basecamp todos sweep --overdue --complete --comment "Cleaning up" --in <project>
```

### Move Card Through Workflow

```bash
# List columns to get IDs
basecamp cards columns --in <project> --json

# Move card to column
basecamp cards move <card_id> --to <column_id> --in <project>
```

### Download File from Basecamp

```bash
basecamp files download <upload_id> --in <project> --out ./downloads
```

## Resource Reference

### Projects

```bash
basecamp projects --json                          # List all
basecamp projects show <id> --json                # Show details
basecamp projects create --name "Name" --json     # Create
basecamp projects update <id> --name "New"        # Update
```

### Todos

```bash
basecamp todos --in <project> --json              # List in project
basecamp todos --assignee me --in <project>       # My todos
basecamp todos --overdue --in <project>           # Overdue only
basecamp todos --status completed --in <project>  # Completed
basecamp todos --list <todolist_id> --in <project> # In specific list
basecamp todo --content "Task" --in <project> --list <list> --assignee me --due tomorrow
basecamp done <id> [id...]                        # Complete (multiple OK)
basecamp reopen <id>                              # Uncomplete
basecamp todos position <id> --to 1               # Move to top
basecamp todos sweep --overdue --complete --comment "Done" --in <project>
```

**Flags:** `--assignee` (todos only - not available on cards/messages), `--status` (completed/pending), `--overdue`, `--list`, `--due`, `--limit`, `--all`

### Todolists

Todolists are containers for todos. Create a todolist before adding todos.

```bash
basecamp todolists --in <project> --json                          # List todolists
basecamp todolists show <id> --in <project>                       # Show details
basecamp todolists create --name "Name" --in <project> --json     # Create
basecamp todolists create --name "Name" --description "Desc" --in <project>
basecamp todolists update <id> --name "New" --in <project>        # Update
```

### Cards (Kanban)

**Note:** Cards do NOT support `--assignee` filtering like todos. Fetch all cards and filter client-side if needed. If a project has multiple card tables, you must specify `--card-table <id>`. When you get an "Ambiguous card table" error, the hint shows available table IDs and names.

```bash
basecamp cards --in <project> --json              # All cards
basecamp cards --card-table <id> --in <project>   # Cards from specific table (required if multiple)
basecamp cards --column <id> --in <project>       # Cards in column
basecamp cards columns --in <project> --json      # List columns (needs --card-table if multiple)
basecamp cards show <id> --in <project>           # Card details
basecamp card --title "Title" --content "<p>Body</p>" --in <project> --column <id>
basecamp cards update <id> --title "New" --due tomorrow --assignee me
basecamp cards move <id> --to <column_id>         # Move to column (numeric ID)
basecamp cards move <id> --to "Done" --card-table <table_id>  # Move by name (needs table)
```

**Identifying completed cards:** Cards in Done columns have `parent.type: "Kanban::DoneColumn"` and `completed: true`. Use this to identify completed cards that haven't been archived.

**Limitation:** Basecamp does not track when cards are moved between columns. The `updated_at` field updates on any modification and cannot reliably indicate when a card was completed.

**Card Steps (checklists):**
```bash
basecamp cards steps <card_id> --in <project>     # List steps
basecamp cards step create --title "Step" --card <id> --in <project>
basecamp cards step complete <step_id> --in <project>
basecamp cards step uncomplete <step_id>
```

**Column management:**
```bash
basecamp cards column show <id> --in <project>
basecamp cards column create --title "Name" --in <project>
basecamp cards column update <id> --title "New"
basecamp cards column move <id> --position 2
basecamp cards column color <id> --color blue
basecamp cards column on-hold <id>                # Enable on-hold section
basecamp cards column watch <id>                  # Subscribe to column
```

### Messages

```bash
basecamp messages --in <project> --json           # List messages
basecamp messages show <id> --in <project>        # Show message
basecamp message --subject "Title" --content "Body" --in <project>
basecamp messages update <id> --subject "New" --content "Updated"
basecamp messages pin <id> --in <project>         # Pin to top
basecamp messages unpin <id>                      # Unpin
```

**Flags:** `--draft` (create as draft), `--message-board <id>` (if multiple boards)

### Comments

```bash
basecamp comments --on <recording_id> --in <project> --json
basecamp comment --content "Text" --on <recording_id> --in <project>
basecamp comments update <id> --content "Updated" --in <project>
```

### Files & Documents

```bash
basecamp files --in <project> --json              # List all (folders, files, docs)
basecamp files --vault <folder_id> --in <project> # List folder contents
basecamp files show <id> --in <project>           # Show item (auto-detects type)
basecamp files download <id> --in <project>       # Download file
basecamp files download <id> --out ./dir          # Download to specific dir
basecamp files folder create --name "Folder" --in <project>
basecamp files doc create --title "Doc" --content "Body" --in <project>
basecamp files doc create --title "Draft" --draft --in <project>
basecamp files update <id> --title "New" --content "Updated"
```

**Subcommands:** `folders`, `uploads`, `documents` (each with pagination flags)

### Schedule

```bash
basecamp schedule --in <project> --json           # Schedule info
basecamp schedule entries --in <project> --json   # List entries
basecamp schedule show <id> --in <project>        # Entry details
basecamp schedule show <id> --date 20240315       # Specific occurrence (recurring)
basecamp schedule create "Event" --starts-at "2024-03-15T09:00:00Z" --ends-at "2024-03-15T10:00:00Z" --in <project>
basecamp schedule create "Meeting" --all-day --notify --participants 1,2,3 --in <project>
basecamp schedule update <id> --summary "New title" --starts-at "..."
basecamp schedule settings --include-due --in <project>  # Include todos/cards due dates
```

**Flags:** `--all-day`, `--notify`, `--participants <ids>`, `--status` (active/archived/trashed)

### Check-ins

```bash
basecamp checkins --in <project> --json           # Questionnaire info
basecamp checkins questions --in <project>        # List questions
basecamp checkins question <id> --in <project>    # Question details
basecamp checkins answers <question_id> --in <project>  # List answers
basecamp checkins answer <id> --in <project>      # Answer details
basecamp checkins question create --title "What did you work on?" --in <project>
basecamp checkins question update <id> --title "New question" --frequency every_week
basecamp checkins answer create --question <id> --content "My answer" --in <project>
basecamp checkins answer update <id> --content "Updated" --in <project>
```

**Schedule options:** `--frequency` (every_day, every_week, every_other_week, every_month, on_certain_days), `--days 1,2,3,4,5` (0=Sun), `--time "5:00pm"`

### Timeline

```bash
basecamp timeline --json                          # Account-wide activity
basecamp timeline --in <project> --json           # Project activity
basecamp timeline me --json                       # Your activity
basecamp timeline --person <id> --json            # Person's activity
basecamp timeline --watch                         # Live monitoring (TUI)
basecamp timeline --watch --interval 60           # Poll every 60 seconds
```

**Note:** `basecamp timeline` (account-wide) works reliably. The `--limit` flag is not supported on timeline commands.

### Recordings (Cross-project)

Use `basecamp recordings <type>` for cross-project queries - this is the only way to query across all projects in one call.

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
basecamp templates --json                         # List templates
basecamp templates show <id> --json               # Template details
basecamp templates create "Template Name"         # Create empty template
basecamp templates update <id> --name "New Name"
basecamp templates delete <id>                    # Trash template
basecamp templates construct <id> --name "New Project"  # Create project (async)
basecamp templates construction <template_id> <construction_id>  # Check status
```

**Construct returns construction_id - poll until status="completed" to get project.**

### Webhooks

```bash
basecamp webhooks --in <project> --json           # List webhooks
basecamp webhooks show <id> --in <project>        # Webhook details
basecamp webhooks create --url "https://..." --in <project>
basecamp webhooks create --url "https://..." --types "Todo,Comment" --in <project>
basecamp webhooks update <id> --active --in <project>
basecamp webhooks update <id> --inactive          # Disable
basecamp webhooks delete <id> --in <project>
```

**Event types:** Todo, Todolist, Message, Comment, Document, Upload, Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer

### Subscriptions

```bash
basecamp subscriptions <recording_id> --in <project>  # Who's subscribed
basecamp subscriptions subscribe <id> --in <project>  # Subscribe yourself
basecamp subscriptions unsubscribe <id>               # Unsubscribe
basecamp subscriptions add <id> --people 1,2,3        # Add people
basecamp subscriptions remove <id> --people 1,2,3     # Remove people
```

### Lineup (Account-wide Markers)

```bash
basecamp lineup create "Milestone" "2024-03-15"   # Create marker
basecamp lineup create --name "Launch" --date tomorrow
basecamp lineup update <id> --name "New Name" --date "+7"
basecamp lineup delete <id>
```

**Note:** Lineup markers are account-wide, not project-scoped.

### Campfire

```bash
basecamp campfire --in <project> --json           # List campfires
basecamp campfire messages --in <project> --json  # List messages
basecamp campfire post --content "Hello!" --in <project>
basecamp campfire line <line_id> --in <project>   # Show line
basecamp campfire delete <line_id> --in <project> # Delete line
```

### People

```bash
basecamp people --json                            # All people in account
basecamp people --in <project> --json             # People on project
basecamp me --json                                # Current user
basecamp people show <id> --json                  # Person details
basecamp people add <id> --in <project>           # Add to project
basecamp people remove <id> --in <project>        # Remove from project
```

### Search

```bash
basecamp search "query" --json                    # Full-text search
basecamp search "query" --sort updated_at --limit 20
basecamp search metadata --json                   # Available search scopes
```

### Generic Show

```bash
basecamp show <type> <id> --in <project> --json   # Show any recording type
# Types: todo, todolist, message, comment, card, card-table, document (or omit <type> for generic lookup)
```

## Configuration

The CLI uses two directory namespaces: `basecamp` for your Basecamp identity and project relationships, `basecamp` for tool-specific operational data.

```
~/.config/basecamp/           # Basecamp identity (DO NOT read credentials)
├── credentials.json          #   OAuth tokens — NEVER read or log
├── client.json               #   DCR client registration
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

**Rate limiting (429):** The CLI handles backoff automatically. If you see 429 errors, reduce request frequency.

**Authentication errors:**
```bash
basecamp auth status                              # Check auth
basecamp auth login                               # Re-authenticate (full access)
basecamp auth login --scope read                  # Read-only access
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

**Invalid flag errors:** All shortcut commands require explicit flags:
- `basecamp todo --content "text"` (not `basecamp todo "text"`)
- `basecamp card --title "title"` (not `basecamp card "title"`)

**URL malformed (curl exit 3):** Special characters in content. Use plain text or properly escaped HTML.

## Learn More

- API concepts: https://github.com/basecamp/bc3-api#key-concepts
- CLI repo: https://github.com/basecamp/basecamp-cli
- API coverage: See API-COVERAGE.md in the CLI repo
