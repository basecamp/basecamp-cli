# Basecamp CLI (`bcq`) - Agent-First Design

**bcq** — "Basecamp Query" — a CLI designed primarily for AI coding agents while remaining intuitive for humans. Inspired by `jq`/`yq`, the name signals: **API-first, tooling-first, agent-first**.

## Agent Compatibility

`bcq` is designed to work with **any AI agent that can execute shell commands**:

| Agent | Integration Level | Notes |
|-------|-------------------|-------|
| **Claude Code** | Full | Hooks, skills, MCP (advanced features) |
| **OpenCode** | Full | CLI + JSON output |
| **Codex** | Full | CLI + JSON output |
| **Any shell-capable agent** | Full | Standard CLI interface |

**Philosophy**: The CLI is the universal foundation. Agent-specific enhancements (Claude Code hooks, skills, MCP) are optional layers on top. No agent is privileged over another for core functionality.

---

## Naming: `bcq`

| Name | Inspiration | Signal |
|------|-------------|--------|
| `bcq` | `jq`, `yq`, `fq` | Query-first, data manipulation, piping |
| | `bc` = Basecamp | Clear association |
| | `q` = query | API/data focus |

```bash
bcq projects | jq '.data[0]'           # Extract from envelope
bcq projects -q | jq '.[0]'            # Quiet mode: raw data
bcq todos --assignee me | bcq fmt      # Pipeline-friendly
```

---

## Core Philosophy: Agent-First Design

### What Would an AI Agent Want?

1. **Instant orientation** — `bcq` with no args → everything needed to start
2. **Predictable patterns** — Learn once, apply everywhere
3. **Rich context** — Equivalent to web UI, in agent-digestible form
4. **Token efficiency** — Dense output, no fluff
5. **Breadcrumbs** — "What can I do next?" after every action
6. **Error recovery** — Errors that help fix the problem
7. **ID/URL-based writes** — Unambiguous, explicit operations

### Output Philosophy

| Context | Format | Reason |
|---------|--------|--------|
| Piped / scripted | JSON envelope | Structured, with context and breadcrumbs |
| TTY / interactive | Markdown | Rich, readable, scannable |
| `--json` | JSON envelope | Force JSON output |
| `--md` | Markdown | Force Markdown output |
| `--quiet` / `--data` | Raw data only | Just `.data`, no envelope |

### JSON Output Contract

**Default (envelope):**
```bash
bcq projects           # Returns envelope with data, summary, breadcrumbs
bcq projects --json    # Forces JSON envelope even in TTY
```

**Quiet mode (data-only):**
```bash
bcq projects --quiet   # Returns raw data array, no envelope
bcq projects -q        # Same as --quiet
bcq projects --data    # Alias for --quiet
```

**Piping examples:**
```bash
bcq projects | jq '.data[0]'           # Extract from envelope
bcq projects --quiet | jq '.[0]'       # Direct array access
bcq projects -q | jq '.[] | .name'     # Iterate raw data
```

---

## Layered Configuration

Configuration builds up from global → local, like git config:

```
~/.config/basecamp/
├── config.json           # Global defaults (all repos)
├── credentials.json      # OAuth tokens
├── client.json           # DCR client registration
└── accounts.json         # Discovered accounts

.basecamp/                # Per-directory/repo configuration
├── config.json           # Local overrides
└── cache/                # Local cache (optional)
```

### Config Hierarchy

```
Global (~/.config/basecamp/config.json)
  └─ Local (.basecamp/config.json)
       └─ Environment variables
            └─ Command-line flags
```

Each layer overrides the previous.

### Configuration Options

```json
// ~/.config/basecamp/config.json (global)
{
  "default_account_id": 12345,
  "output_format": "auto",      // auto | json | markdown
  "color": true,
  "pager": "less -R"
}

// .basecamp/config.json (per-directory)
{
  "project_id": 67890,
  "project_name": "Basecamp 4",    // For display
  "todolist_id": 11111,            // Default todolist
  "todolist_name": "Development",
  "card_table_id": 22222,          // Default card table
  "campfire_id": 33333,            // Default campfire
  "team": ["@jeremy", "@david"]    // Quick @-mention completion
}
```

### Config Commands

```bash
bcq config                         # Show effective config
bcq config --global                # Show global only
bcq config --local                 # Show local only

bcq config init                    # Interactive: create .basecamp/config.json
bcq config set project 67890       # Set locally
bcq config set project 67890 --global  # Set globally
bcq config unset project           # Remove local override

bcq config project                 # Interactive project picker
bcq config todolist                # Interactive todolist picker
bcq config campfire                # Interactive campfire picker
```

---

## Quick-Start Mode (No Arguments)

```
$ bcq

bcq v0.1.0 — Basecamp Query
Auth: ✓ jeremy@37signals.com @ 37signals

QUICK START
  bcq projects              List projects
  bcq todos                 Your assigned todos
  bcq search "query"        Find anything

COMMON TASKS
  bcq todo "content"        Create todo
  bcq done <id>             Complete todo
  bcq comment "text" <id>   Add comment

CONTEXT
  Account: 37signals (12345)
  Project: Basecamp 4 (67890)      ← from .basecamp/config.json
  Todolist: Development (11111)

NEXT: bcq todos
```

### Fast Mode (No State Fetching)

Quick-start can optionally fetch live counts (todos assigned, unread messages, etc.), but this requires API calls and can be slow for agents:

```bash
bcq                    # Default: no API calls, just orientation
bcq --state            # Include live counts (requires API calls)
bcq --fast             # Explicit: no state fetching (same as default)
```

The default prioritizes speed for agents. Use `--state` when you want live statistics.

---

## Response Structure

### Universal Envelope (JSON)

```json
{
  "ok": true,
  "data": [ ... ],
  "summary": "47 todos assigned to you",
  "context": {
    "account": {"id": 12345, "name": "37signals"},
    "project": {"id": 67890, "name": "Basecamp 4"},
    "todolist": {"id": 11111, "name": "Development"}
  },
  "breadcrumbs": [
    {"action": "create", "cmd": "bcq todo \"content\""},
    {"action": "filter", "cmd": "bcq todos --status completed"},
    {"action": "search", "cmd": "bcq search \"query\""}
  ],
  "meta": {
    "total": 47,
    "showing": 25,
    "page": 1,
    "next": "bcq todos --page 2"
  }
}
```

### Markdown Mode

```markdown
## Your Todos (47)

| # | Content | Due | Assignee |
|---|---------|-----|----------|
| 123 | Fix login bug | Jan 15 | @jeremy |
| 124 | Update docs | Jan 20 | @david |

*Showing 25 of 47* — `bcq todos --page 2` for more

### Actions
- Create: `bcq todo "content"`
- Complete: `bcq done <id>`
- Filter: `bcq todos --status completed`
```

---

## Commands

### Query Commands (Read)

```bash
bcq                                # Quick-start
bcq projects                       # List projects
bcq todos                          # Assigned todos (uses context)
bcq todos --all                    # All todos in project
bcq todos --in "Basecamp 4"        # In specific project
bcq todos --list "Development"     # In specific todolist
bcq search "query"                 # Global search
bcq show todo 123                  # Full todo details
bcq show project "Basecamp 4"      # Full project details
bcq people                         # List people
bcq campfire                       # Recent campfire messages
```

### Action Commands (Write)

**Write commands require explicit IDs or full Basecamp URLs. No name resolution for writes.**

```bash
bcq todo "Fix the login bug"                    # Create (uses context project/todolist)
bcq todo "Fix bug" --project 67890              # Create with explicit project ID
bcq todo "Fix bug" --project 67890 --list 11111 # Create with explicit todolist ID
bcq done 123                                    # Complete todo by ID
bcq done 123 124 125                            # Complete multiple
bcq reopen 123                                  # Reopen by ID
bcq comment "LGTM" --on 123                     # Add comment by recording ID
bcq assign 123 --to 456                         # Reassign by person ID
bcq say "Hello!" --campfire 33333               # Send campfire message by ID
```

**Why ID-only for writes?**
- Prevents accidental writes to wrong resources
- Eliminates ambiguity errors during write operations
- Name resolution can fail; writes should be deterministic
- Agents should resolve names in read operations, then use IDs for writes

**Basecamp URLs work too:**
```bash
bcq done https://3.basecamp.com/12345/buckets/67890/todos/123
```

### Utility Commands

```bash
bcq config                         # Show/set configuration
bcq auth login                     # OAuth flow (browser)
bcq auth login --no-browser        # OAuth flow (manual code entry)
bcq auth logout                    # Clear credentials
bcq auth status                    # Auth info
bcq fmt                            # Format stdin (like jq's .)
bcq version                        # Version info
```

### Global Flags

```bash
--json, -j           # Force JSON envelope output
--md, -m             # Force Markdown output
--quiet, -q          # Raw data only (no envelope)
--data               # Alias for --quiet
--verbose, -v        # Debug output
--project, -p ID     # Override project context
--account, -a ID     # Override account
--help, -h           # Help
```

---

## Authentication

### OAuth 2.1 with Dynamic Client Registration

`bcq` uses OAuth 2.1 with DCR targeting the Basecamp OAuth endpoint (bc3 oauth branch), with `.well-known` discovery.

```bash
# Standard flow (opens browser)
bcq auth login

# Headless/remote flow (manual code entry)
bcq auth login --no-browser
# Prints: Visit https://3.basecamp.com/oauth/authorize?...
# Prints: Enter authorization code: [user pastes code]
```

### Token File Security

Credentials stored at `~/.config/basecamp/credentials.json` with permissions `0600` (owner read/write only).

### Account Resolution

Account ID is required for all API calls. Resolution order:

1. `--account` flag
2. `BASECAMP_ACCOUNT_ID` environment variable
3. `.basecamp/config.json` → `account_id`
4. `~/.config/basecamp/config.json` → `account_id`
5. Auto-discovered from token (if only one account)

**Fail fast with clear hints:**
```json
{
  "ok": false,
  "error": "No account configured",
  "code": "auth_required",
  "hint": "Run: bcq auth login",
  "accounts": [
    {"id": 12345, "name": "37signals"},
    {"id": 67890, "name": "Side Project"}
  ]
}
```

---

## Name Resolution (Read Commands Only)

**Names are supported for read operations; write operations require IDs.**

```bash
# Read commands support names:
bcq todos --in "Basecamp 4"        # By name
bcq todos --in 67890               # By ID
bcq show project "Basecamp 4"      # By name
bcq people --search "@david"       # By @handle

# Write commands require IDs:
bcq done 123                       # ID required
bcq comment "Done!" --on 123       # ID required
bcq assign 123 --to 456            # IDs required
```

### Workflow: Resolve Then Act

```bash
# Step 1: Find the resource (read, supports names)
bcq show project "Basecamp 4" --quiet | jq '.id'
# → 67890

# Step 2: Act on it (write, requires ID)
bcq todo "Fix bug" --project 67890
```

### Ambiguous Name Handling

When a name matches multiple resources:
```json
{
  "ok": false,
  "error": "Ambiguous project name",
  "code": "ambiguous",
  "matches": [
    {"id": 67890, "name": "Basecamp 4"},
    {"id": 11111, "name": "Basecamp Classic"}
  ],
  "hint": "Use --project 67890 or more specific name"
}
```

---

## Rich Detail Views

```bash
$ bcq show todo 123 --md
```

```markdown
## Todo #123: Fix the login bug

| Field | Value |
|-------|-------|
| Project | Basecamp 4 > Development |
| Status | Active |
| Assignee | @jeremy |
| Due | January 15, 2024 (in 5 days) |
| Created | January 10 by @david |

### Description
Login form throws 500 when email contains "+".

### Comments (2)
| When | Who | Comment |
|------|-----|---------|
| Jan 11 | @jeremy | On it, fix by EOD. |
| Jan 10 | @david | Can you look at this? |

### Actions
- `bcq done 123` — Complete
- `bcq comment "text" --on 123` — Comment
- `bcq assign 123 --to @name` — Reassign
```

---

## Error Design

### Helpful, Actionable Errors

```bash
$ bcq show todo 99999
```

```json
{
  "ok": false,
  "error": "Todo not found",
  "code": "not_found",
  "searched": {"type": "todo", "id": 99999},
  "suggestions": [
    {"id": 999, "content": "Fix header", "project": "Basecamp 4"},
    {"id": 9999, "content": "Update docs", "project": "Marketing"}
  ],
  "hint": "Try: bcq search \"your keywords\""
}
```

### Error Codes

| Code | Exit | Meaning |
|------|------|---------|
| `success` | 0 | OK |
| `usage` | 1 | Bad arguments |
| `not_found` | 2 | Resource not found |
| `auth_required` | 3 | Need to login |
| `forbidden` | 4 | Permission denied |
| `rate_limit` | 5 | Too many requests |
| `network` | 6 | Connection failed |
| `api_error` | 7 | Server error |
| `ambiguous` | 8 | Multiple matches for name |

---

## Architecture

```
basecamp-cli/
├── bin/
│   └── bcq                       # Entry point
├── lib/
│   ├── core.sh                   # Output, breadcrumbs, config
│   ├── auth.sh                   # OAuth 2.1 + DCR
│   ├── api.sh                    # HTTP, pagination, rate limiting
│   ├── names.sh                  # Name → ID resolution
│   ├── config.sh                 # Layered config handling
│   └── commands/
│       ├── quick_start.sh
│       ├── projects.sh
│       ├── todos.sh
│       ├── show.sh
│       ├── search.sh
│       ├── config.sh
│       └── auth.sh
├── test/
│   ├── *.bats                    # BATS tests
│   └── fixtures/                 # Mock responses
├── completions/
│   ├── bcq.bash
│   └── bcq.zsh
└── docs/
    └── README.md
```

---

## Implementation Phases

### Phase 1: Foundation
- [ ] Entry point + format auto-detection
- [ ] Quick-start mode (no args)
- [ ] Response envelope (ok, data, summary, breadcrumbs)
- [ ] Layered config system
- [ ] OAuth 2.1 auth flow

### Phase 2: Core Queries
- [ ] `bcq projects`
- [ ] `bcq todos` (with context awareness)
- [ ] `bcq show todo/project`
- [ ] `bcq search`

### Phase 3: Core Actions
- [ ] `bcq todo "content"`
- [ ] `bcq done`
- [ ] `bcq comment`
- [ ] `bcq assign`

### Phase 4: Agent Ergonomics
- [ ] Name resolution (projects, people)
- [ ] Breadcrumb generation
- [ ] Error suggestions
- [ ] Context memory

### Phase 5: Polish
- [ ] Tab completion
- [ ] Rate limiting + retry
- [ ] Comprehensive tests
- [ ] Documentation

---

## Test Requirements

Every command must have:
1. **Happy path test** — Normal usage works
2. **Error path tests** — Proper error codes and messages
3. **Output format tests** — JSON and Markdown both correct
4. **Context tests** — Respects config hierarchy

```bash
# test/todos.bats

@test "bcq todos returns JSON array when piped" {
  run bash -c 'bcq todos | head -c1'
  assert_output '['
}

@test "bcq todos respects .basecamp/config.json project" {
  mkdir -p .basecamp
  echo '{"project_id": 123}' > .basecamp/config.json
  run bcq todos --json
  assert_output --partial '"project":{"id":123'
}

@test "bcq todo requires content" {
  run bcq todo
  assert_failure
  assert_output --partial '"code":"usage"'
}
```

---

## Success Criteria

### Universal (All Agents)
1. **Agent orients in 1 command**: `bcq` → knows what to do
2. **Task completion in 1-2 commands**: Create todo, complete todo
3. **Error recovery**: Errors suggest fixes with actionable hints
4. **Web UI parity**: Detail views show everything humans see
5. **Token efficiency**: Dense output, no fluff
6. **Breadcrumbs**: Every response guides next action
7. **Config layering**: Global + local settings work correctly

### Agent-Specific Enhancements (Optional)
8. **Claude Code**: Hooks for commit linking, `/basecamp` skill
9. **MCP**: Structured tool definitions for advanced agents
10. **Future agents**: Extensible design accommodates new capabilities

---

## Interoperability Design

### Universal Interface (Required)
Every agent gets these capabilities via standard CLI:
- **JSON output**: `bcq todos --json` → parseable data
- **Exit codes**: Semantic success/failure
- **Error format**: JSON errors with `code`, `hint`
- **Help text**: `bcq --help`, `bcq <cmd> --help`
- **Piping**: Works with jq, grep, xargs, etc.

### Claude Code Enhancements (Optional)
For Claude Code users who want deeper integration:
- **Hooks**: Auto-link commits to Basecamp todos
- **Skills**: `/basecamp` slash command with context awareness
- **MCP server**: Structured tools with rich schemas

### OpenCode / Codex / Others
These agents use the same CLI interface. As they add features (hooks, plugins, MCP), we can add support. The foundation is agent-agnostic.

---

## MCP Server: `bcq mcp serve`

When MCP integration is compelling and delightful, `bcq` itself can serve as an MCP server:

```bash
# Start MCP server on stdio (for local agents)
bcq mcp serve

# Start MCP server on port (for remote agents)
bcq mcp serve --port 8080

# Configure in .mcp.json
{
  "basecamp": {
    "command": "bcq",
    "args": ["mcp", "serve"]
  }
}
```

**Why built-in?**
- Single tool to install and maintain
- CLI and MCP share the same code, auth, config
- No separate MCP server package to manage
- `bcq` commands become MCP tools automatically

### Command Schema (Source of Truth)

Commands are defined in `lib/schema.json`, which drives:
1. CLI argument parsing and validation
2. CLI help text generation
3. MCP tool definitions

```json
// lib/schema.json
{
  "commands": {
    "todos": {
      "description": "List todos",
      "category": "query",
      "args": {
        "project": {
          "type": "integer",
          "aliases": ["-p", "--project", "--in"],
          "description": "Project ID"
        },
        "assignee": {
          "type": "string",
          "aliases": ["-a", "--assignee"],
          "description": "Filter by assignee (ID, @handle, or 'me')"
        },
        "status": {
          "type": "string",
          "enum": ["active", "completed"],
          "aliases": ["-s", "--status"],
          "description": "Filter by status"
        }
      }
    },
    "done": {
      "description": "Complete todo(s)",
      "category": "action",
      "args": {
        "id": {
          "type": "integer",
          "required": true,
          "positional": true,
          "variadic": true,
          "description": "Todo ID(s) to complete"
        }
      }
    }
  }
}
```

### MCP Output Mode

In MCP mode, all responses use the JSON envelope format:

```bash
bcq mcp serve  # Forces --json for all tool responses
```

**When to use MCP vs CLI?**
| Scenario | CLI | MCP |
|----------|-----|-----|
| Quick queries | ✓ | |
| Piping/scripting | ✓ | |
| Structured tool calling | | ✓ |
| Rich type schemas | | ✓ |
| Resource subscriptions | | ✓ |
| Real-time updates | | ✓ |

The CLI remains the foundation. `bcq mcp serve` is an adapter for MCP-native agents.
