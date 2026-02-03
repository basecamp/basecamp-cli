---
name: basecamp
description: |
  Interact with Basecamp via bcq CLI. Full API coverage: projects, todos, cards,
  messages, files, schedule, check-ins, timeline, recordings, templates, webhooks,
  subscriptions, lineup, and campfire. Use for ANY Basecamp question or action.
triggers:
  # Direct invocations
  - basecamp
  - /basecamp
  - bcq
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

Full bcq CLI coverage: 130 endpoints across todos, cards, messages, files, schedule, check-ins, timeline, recordings, templates, webhooks, subscriptions, lineup, and campfire.

## Agent Invariants

**MUST follow these rules:**

1. **Always use `--json`** for structured, predictable output
2. **Parse URLs first** with `bcq url parse "<url>"` to extract IDs
3. **Comments are flat** - reply to parent recording, not to comments
4. **Check context** via `.basecamp/config.json` before assuming project
5. **Rich text fields accept Markdown** - bcq converts to HTML automatically
6. **Project scope is mandatory** - `--in <project>` is required for resource queries (todos, cards, messages, etc.). There is no cross-project query mode. For cross-project data, use `bcq recordings <type>` or loop through projects individually.

### Output Modes

```bash
bcq <cmd> --json      # JSON envelope with data, summary, breadcrumbs (recommended)
bcq <cmd> --quiet     # Raw JSON data only, no envelope
bcq <cmd> --agent     # Machine-readable, no interactive prompts
bcq <cmd> --ids-only  # Just IDs, one per line
bcq <cmd> --count     # Just the count
bcq <cmd> --stats     # Include session stats in output
```

### Pagination

```bash
bcq <cmd> --limit 50   # Cap results (default varies by resource)
bcq <cmd> --all        # Fetch all (may be slow for large datasets)
bcq <cmd> --page 1     # First page only, no auto-pagination
```

`--all` and `--limit` are mutually exclusive. `--page` cannot combine with either.

### Smart Defaults

- `--assignee me` resolves to current user
- `--due tomorrow` / `--due +3` / `--due "next week"` - natural date parsing
- Project from `.basecamp/config.json` if `--in` not specified

## Quick Reference

> **Note:** Most queries require `--in <project>`. For cross-project data, use `bcq recordings <type>` or loop through projects individually.

| Task | Command |
|------|---------|
| List projects | `bcq projects --json` |
| My todos (in project) | `bcq todos --assignee me --in <project> --json` |
| All todos (cross-project) | `bcq recordings todos --json` (filter client-side) |
| Overdue todos | `bcq todos --overdue --in <project> --json` |
| Create todo | `bcq todo --content "Task" --in <project> --list <list> --json` |
| Create todolist | `bcq todolists create --name "Name" --in <project> --json` |
| Complete todo | `bcq done <id> --json` |
| List cards | `bcq cards --in <project> --json` |
| Create card | `bcq card --title "Title" --in <project> --json` |
| Move card | `bcq cards move <id> --to <column> --in <project> --json` |
| Post message | `bcq message --subject "Title" --content "Body" --in <project> --json` |
| Post to campfire | `bcq campfire post --content "Message" --in <project> --json` |
| Add comment | `bcq comment --content "Text" --on <recording_id> --in <project> --json` |
| Search | `bcq search "query" --json` |
| Parse URL | `bcq url parse "<url>" --json` |
| Download file | `bcq files download <id> --in <project>` |
| Watch timeline | `bcq timeline --watch` |

## URL Parsing

**Always parse URLs before acting on them:**

```bash
bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598" --json
```

Returns: `account_id`, `bucket_id`, `type`, `recording_id`, `comment_id` (from fragment).

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
bcq url parse "https://...messages/123#__recording_456" --json
# Returns recording_id: 123 (parent), comment_id: 456 (fragment) - comment on 123, not 456
bcq comment --content "Reply" --on 123 --in <project>
```

## Decision Trees

### Finding Content

```
Need to find something?
├── Know the type + project? → bcq <type> --in <project> --json
├── Need cross-project data? → bcq recordings <type> --json (ONLY cross-project option)
│   (types: todos, messages, documents, comments, cards, uploads)
├── Full-text search? → bcq search "query" --json
└── Have a URL? → bcq url parse "<url>" --json
```

### Modifying Content

```
Want to change something?
├── Have URL? → bcq url parse "<url>" → use extracted IDs
├── Have ID? → bcq <resource> update <id> --field value
├── Change status? → bcq recordings trash|archive|restore <id>
└── Complete todo? → bcq done <id>
```

## Common Workflows

### Link Code to Basecamp Todo

```bash
# Get commit info and comment on todo (use printf %q for safe quoting)
COMMIT=$(git rev-parse --short HEAD)
MSG=$(git log -1 --format=%s)
bcq comment --content "Commit $COMMIT: $(printf '%s' "$MSG")" --on <todo_id> --in <project>

# Complete when done
bcq done <todo_id>
```

### Track PR in Basecamp

```bash
# Create todo for PR work
bcq todo --content "Review PR #42" --in <project> --assignee me --due tomorrow

# When merged
bcq done <todo_id>
bcq campfire post --content "Merged PR #42" --in <project>
```

### Bulk Process Overdue Todos

```bash
# Preview overdue todos
bcq todos sweep --overdue --dry-run --in <project>

# Complete all with comment
bcq todos sweep --overdue --complete --comment "Cleaning up" --in <project>
```

### Move Card Through Workflow

```bash
# List columns to get IDs
bcq cards columns --in <project> --json

# Move card to column
bcq cards move <card_id> --to <column_id> --in <project>
```

### Download File from Basecamp

```bash
bcq files download <upload_id> --in <project> --out ./downloads
```

## Resource Reference

### Projects

```bash
bcq projects --json                          # List all
bcq projects show <id> --json                # Show details
bcq projects create --name "Name" --json     # Create
bcq projects update <id> --name "New"        # Update
```

### Todos

```bash
bcq todos --in <project> --json              # List in project
bcq todos --assignee me --in <project>       # My todos
bcq todos --overdue --in <project>           # Overdue only
bcq todos --status completed --in <project>  # Completed
bcq todos --list <todolist_id> --in <project> # In specific list
bcq todo --content "Task" --in <project> --list <list> --assignee me --due tomorrow
bcq done <id> [id...]                        # Complete (multiple OK)
bcq reopen <id>                              # Uncomplete
bcq todos position <id> --to 1               # Move to top
bcq todos sweep --overdue --complete --comment "Done" --in <project>
```

**Flags:** `--assignee` (todos only - not available on cards/messages), `--status` (completed/pending), `--overdue`, `--list`, `--due`, `--limit`, `--all`

### Todolists

Todolists are containers for todos. Create a todolist before adding todos.

```bash
bcq todolists --in <project> --json                          # List todolists
bcq todolists show <id> --in <project>                       # Show details
bcq todolists create --name "Name" --in <project> --json     # Create
bcq todolists create --name "Name" --description "Desc" --in <project>
bcq todolists update <id> --name "New" --in <project>        # Update
```

### Cards (Kanban)

**Note:** Cards do NOT support `--assignee` filtering like todos. Fetch all cards and filter client-side if needed. If a project has multiple card tables, you must specify `--card-table <id>`. When you get an "Ambiguous card table" error, the hint shows available table IDs and names.

```bash
bcq cards --in <project> --json              # All cards
bcq cards --card-table <id> --in <project>   # Cards from specific table (required if multiple)
bcq cards --column <id> --in <project>       # Cards in column
bcq cards columns --in <project> --json      # List columns (needs --card-table if multiple)
bcq cards show <id> --in <project>           # Card details
bcq card --title "Title" --content "<p>Body</p>" --in <project> --column <id>
bcq cards update <id> --title "New" --due tomorrow --assignee me
bcq cards move <id> --to <column_id>         # Move to column (numeric ID)
bcq cards move <id> --to "Done" --card-table <table_id>  # Move by name (needs table)
```

**Card Steps (checklists):**
```bash
bcq cards steps <card_id> --in <project>     # List steps
bcq cards step create --title "Step" --card <id> --in <project>
bcq cards step complete <step_id> --in <project>
bcq cards step uncomplete <step_id>
```

**Column management:**
```bash
bcq cards column show <id> --in <project>
bcq cards column create --title "Name" --in <project>
bcq cards column update <id> --title "New"
bcq cards column move <id> --position 2
bcq cards column color <id> --color blue
bcq cards column on-hold <id>                # Enable on-hold section
bcq cards column watch <id>                  # Subscribe to column
```

### Messages

```bash
bcq messages --in <project> --json           # List messages
bcq messages show <id> --in <project>        # Show message
bcq message --subject "Title" --content "Body" --in <project>
bcq messages update <id> --subject "New" --content "Updated"
bcq messages pin <id> --in <project>         # Pin to top
bcq messages unpin <id>                      # Unpin
```

**Flags:** `--draft` (create as draft), `--message-board <id>` (if multiple boards)

### Comments

```bash
bcq comments --on <recording_id> --in <project> --json
bcq comment --content "Text" --on <recording_id> --in <project>
bcq comments update <id> --content "Updated" --in <project>
```

### Files & Documents

```bash
bcq files --in <project> --json              # List all (folders, files, docs)
bcq files --vault <folder_id> --in <project> # List folder contents
bcq files show <id> --in <project>           # Show item (auto-detects type)
bcq files download <id> --in <project>       # Download file
bcq files download <id> --out ./dir          # Download to specific dir
bcq files folder create --name "Folder" --in <project>
bcq files doc create --title "Doc" --content "Body" --in <project>
bcq files doc create --title "Draft" --draft --in <project>
bcq files update <id> --title "New" --content "Updated"
```

**Subcommands:** `folders`, `uploads`, `documents` (each with pagination flags)

### Schedule

```bash
bcq schedule --in <project> --json           # Schedule info
bcq schedule entries --in <project> --json   # List entries
bcq schedule show <id> --in <project>        # Entry details
bcq schedule show <id> --date 20240315       # Specific occurrence (recurring)
bcq schedule create "Event" --starts-at "2024-03-15T09:00:00Z" --ends-at "2024-03-15T10:00:00Z" --in <project>
bcq schedule create "Meeting" --all-day --notify --participants 1,2,3 --in <project>
bcq schedule update <id> --summary "New title" --starts-at "..."
bcq schedule settings --include-due --in <project>  # Include todos/cards due dates
```

**Flags:** `--all-day`, `--notify`, `--participants <ids>`, `--status` (active/archived/trashed)

### Check-ins

```bash
bcq checkins --in <project> --json           # Questionnaire info
bcq checkins questions --in <project>        # List questions
bcq checkins question <id> --in <project>    # Question details
bcq checkins answers <question_id> --in <project>  # List answers
bcq checkins answer <id> --in <project>      # Answer details
bcq checkins question create --title "What did you work on?" --in <project>
bcq checkins question update <id> --title "New question" --frequency every_week
bcq checkins answer create --question <id> --content "My answer" --in <project>
bcq checkins answer update <id> --content "Updated" --in <project>
```

**Schedule options:** `--frequency` (every_day, every_week, every_other_week, every_month, on_certain_days), `--days 1,2,3,4,5` (0=Sun), `--time "5:00pm"`

### Timeline

```bash
bcq timeline --json                          # Account-wide activity
bcq timeline --in <project> --json           # Project activity
bcq timeline me --json                       # Your activity
bcq timeline --person <id> --json            # Person's activity
bcq timeline --watch                         # Live monitoring (TUI)
bcq timeline --watch --interval 60           # Poll every 60 seconds
```

### Recordings (Cross-project)

```bash
bcq recordings todos --json                  # All todos across projects
bcq recordings messages --in <project>       # Messages in project
bcq recordings documents --status archived   # Archived docs
bcq recordings cards --sort created_at --direction asc
```

**Types:** `todos`, `messages`, `documents`, `comments`, `cards`, `uploads`

**Status management:**
```bash
bcq recordings trash <id> --in <project>     # Move to trash
bcq recordings archive <id> --in <project>   # Archive
bcq recordings restore <id> --in <project>   # Restore to active
bcq recordings visibility <id> --visible --in <project>  # Show to clients
bcq recordings visibility <id> --hidden      # Hide from clients
```

### Templates

```bash
bcq templates --json                         # List templates
bcq templates show <id> --json               # Template details
bcq templates create "Template Name"         # Create empty template
bcq templates update <id> --name "New Name"
bcq templates delete <id>                    # Trash template
bcq templates construct <id> --name "New Project"  # Create project (async)
bcq templates construction <template_id> <construction_id>  # Check status
```

**Construct returns construction_id - poll until status="completed" to get project.**

### Webhooks

```bash
bcq webhooks --in <project> --json           # List webhooks
bcq webhooks show <id> --in <project>        # Webhook details
bcq webhooks create --url "https://..." --in <project>
bcq webhooks create --url "https://..." --types "Todo,Comment" --in <project>
bcq webhooks update <id> --active --in <project>
bcq webhooks update <id> --inactive          # Disable
bcq webhooks delete <id> --in <project>
```

**Event types:** Todo, Todolist, Message, Comment, Document, Upload, Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer

### Subscriptions

```bash
bcq subscriptions <recording_id> --in <project>  # Who's subscribed
bcq subscriptions subscribe <id> --in <project>  # Subscribe yourself
bcq subscriptions unsubscribe <id>               # Unsubscribe
bcq subscriptions add <id> --people 1,2,3        # Add people
bcq subscriptions remove <id> --people 1,2,3     # Remove people
```

### Lineup (Account-wide Markers)

```bash
bcq lineup create "Milestone" "2024-03-15"   # Create marker
bcq lineup create --name "Launch" --date tomorrow
bcq lineup update <id> --name "New Name" --date "+7"
bcq lineup delete <id>
```

**Note:** Lineup markers are account-wide, not project-scoped.

### Campfire

```bash
bcq campfire --in <project> --json           # List campfires
bcq campfire messages --in <project> --json  # List messages
bcq campfire post --content "Hello!" --in <project>
bcq campfire line <line_id> --in <project>   # Show line
bcq campfire delete <line_id> --in <project> # Delete line
```

### People

```bash
bcq people --json                            # All people in account
bcq people --in <project> --json             # People on project
bcq me --json                                # Current user
bcq people show <id> --json                  # Person details
bcq people add <id> --in <project>           # Add to project
bcq people remove <id> --in <project>        # Remove from project
```

### Search

```bash
bcq search "query" --json                    # Full-text search
bcq search "query" --sort updated_at --limit 20
bcq search metadata --json                   # Available search scopes
```

### Generic Show

```bash
bcq show <type> <id> --in <project> --json   # Show any recording type
# Types: todo, todolist, message, comment, card, card-table, document (or omit <type> for generic lookup)
```

## Configuration

**Per-repo config:** `.basecamp/config.json`
```json
{
  "project_id": "12345",
  "todolist_id": "67890"
}
```

**Initialize:**
```bash
bcq config init
bcq config set project_id <id>
bcq config set todolist_id <id>
```

**Check context:**
```bash
cat .basecamp/config.json 2>/dev/null || echo "No project configured"
```

**Global config:** `~/.config/basecamp/config.json` (account_id, preferences)

## Error Handling

**Rate limiting (429):** bcq handles backoff automatically. If you see 429 errors, reduce request frequency.

**Authentication errors:**
```bash
bcq auth status                              # Check auth
bcq auth login                               # Re-authenticate (full access)
bcq auth login --scope read                  # Read-only access
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
bcq auth status                              # Verify auth working
cat ~/.config/basecamp/accounts.json         # Check available accounts
```

**Invalid flag errors:** All shortcut commands require explicit flags:
- `bcq todo --content "text"` (not `bcq todo "text"`)
- `bcq card --title "title"` (not `bcq card "title"`)

**URL malformed (curl exit 3):** Special characters in content. Use plain text or properly escaped HTML.

## Learn More

- API concepts: https://github.com/basecamp/bc3-api#key-concepts
- bcq repo: https://github.com/basecamp/bcq
- API coverage: See API-COVERAGE.md in bcq repo
