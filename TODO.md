# bcq Development Progress

Track implementation progress here. Check off items as completed.

**Note**: Core CLI is agent-agnostic (works with Claude Code, OpenCode, Codex, any shell-capable agent). Agent-specific features (Claude hooks, MCP) are in "Future/Nice-to-Have".

---

## Phase 1: Foundation

### Entry Point & Core
- [ ] `bin/bcq` — Main entry point with arg parsing
- [ ] `lib/core.sh` — Shared utilities
  - [ ] `detect_output_format()` — auto/json/md based on TTY/pipe
  - [ ] `json_response()` — Build response envelope
  - [ ] `md_response()` — Build markdown output
  - [ ] `breadcrumb()` — Add action suggestions
  - [ ] `die()` — Error exit with proper code

### Quick-Start Mode
- [ ] No-args handler shows orientation
- [ ] Auth status check
- [ ] Context summary (account, project, stats)
- [ ] Next action suggestion

### Configuration System
- [ ] `lib/config.sh` — Config loading/saving
- [ ] Global config: `~/.config/basecamp/config.json`
- [ ] Local config: `.basecamp/config.json`
- [ ] Config merge (local overrides global)
- [ ] `bcq config` — Show effective config
- [ ] `bcq config init` — Interactive setup
- [ ] `bcq config set <key> <value>` — Set config

### Authentication
- [ ] `lib/auth.sh` — OAuth handling
- [ ] Dynamic Client Registration (DCR)
- [ ] PKCE flow
- [ ] Browser open + localhost callback
- [ ] Token storage
- [ ] Token refresh
- [ ] Token introspection (account discovery)
- [ ] `bcq auth login`
- [ ] `bcq auth logout`
- [ ] `bcq auth status`
- [ ] Fallback: `BASECAMP_ACCESS_TOKEN` env var

### API Layer
- [ ] `lib/api.sh` — HTTP helpers
- [ ] `api_get()` — GET with auth
- [ ] `api_post()` — POST with auth
- [ ] `api_put()` — PUT with auth
- [ ] `api_delete()` — DELETE with auth
- [ ] Pagination handling
- [ ] Rate limit detection
- [ ] Retry with backoff

---

## Phase 2: Core Queries

### Projects
- [ ] `bcq projects` — List all projects
- [ ] `bcq show project <id>` — Project details
- [ ] `bcq show project "Name"` — By name

### Todos
- [ ] `bcq todos` — List assigned todos
- [ ] `bcq todos --all` — All todos in project
- [ ] `bcq todos --in <project>` — Filter by project
- [ ] `bcq todos --list <todolist>` — Filter by list
- [ ] `bcq todos --status <status>` — Filter by status
- [ ] `bcq show todo <id>` — Full todo details

### Search
- [ ] `bcq search "query"` — Global search
- [ ] `bcq search "query" --in <project>` — Scoped search

### People
- [ ] `bcq people` — List people
- [ ] `bcq people --in <project>` — Project members
- [ ] `bcq me` — Current user info

---

## Phase 3: Core Actions

### Todo Management
- [ ] `bcq todo "content"` — Create todo
- [ ] `bcq todo "content" --in <project>` — Create in project
- [ ] `bcq todo "content" --list <todolist>` — Create in list
- [ ] `bcq done <id>` — Complete todo
- [ ] `bcq done <id> <id> <id>` — Complete multiple
- [ ] `bcq reopen <id>` — Reopen todo
- [ ] `bcq assign <id> --to <person>` — Assign
- [ ] `bcq unassign <id>` — Remove assignment

### Comments
- [ ] `bcq comment "text" --on <id>` — Add comment

---

## Phase 4: Agent Ergonomics

### Name Resolution
- [ ] `lib/names.sh` — Name → ID lookup
- [ ] Project name resolution
- [ ] Person name resolution (@handle, email)
- [ ] Fuzzy matching
- [ ] Ambiguity handling (error with suggestions)

### Breadcrumbs
- [ ] Auto-generate relevant next actions
- [ ] Context-aware suggestions
- [ ] Include in every response

### Error Suggestions
- [ ] Not found → suggest similar
- [ ] Permission denied → explain why
- [ ] Rate limit → show retry timing

### Context Memory
- [ ] Remember recent project
- [ ] Smart defaults based on history

---

## Phase 5: Polish

### Completion
- [ ] `completions/bcq.bash` — Bash completion
- [ ] `completions/bcq.zsh` — Zsh completion

### Rate Limiting
- [ ] Detect 429 responses
- [ ] Parse Retry-After header
- [ ] Exponential backoff
- [ ] Max retry limit

### Tests
- [ ] `test/test_helper.bash` — Test setup
- [ ] `test/mock_server/` — API mock
- [ ] `test/core.bats` — Core function tests
- [ ] `test/config.bats` — Config tests
- [ ] `test/auth.bats` — Auth tests
- [ ] `test/projects.bats` — Project tests
- [ ] `test/todos.bats` — Todo tests
- [ ] `test/search.bats` — Search tests
- [ ] `test/errors.bats` — Error handling tests

### Documentation
- [ ] `README.md` — User guide
- [ ] `--help` for all commands
- [ ] Examples in help text

### CI
- [ ] `.github/workflows/test.yml` — Run tests on push
- [ ] Lint with shellcheck

---

## Future / Nice-to-Have

### More Basecamp Resources
- [ ] Campfire support (`bcq campfire`, `bcq say`)
- [ ] Card tables (`bcq cards`, `bcq card`)
- [ ] Schedules (`bcq schedule`, `bcq event`)
- [ ] Documents (`bcq docs`, `bcq doc`)
- [ ] Message board (`bcq messages`, `bcq message`)
- [ ] Webhooks (`bcq webhooks`)

### MCP Server (Built-in)
- [ ] `bcq mcp serve` — Start MCP server on stdio
- [ ] `bcq mcp serve --port 8080` — Start on HTTP port
- [ ] Auto-generate MCP tools from CLI commands
- [ ] Share auth/config between CLI and MCP

### Agent-Specific Enhancements
- [ ] **Claude Code**: Hooks for commit → todo linking
- [ ] **Claude Code**: `/basecamp` skill with context awareness
- [ ] **OpenCode/Codex**: Plugin/integration as they add support

### Infrastructure
- [ ] Caching layer
- [ ] Offline mode
- [ ] Activity feed streaming (would benefit from MCP)

---

## Notes

<!-- Add implementation notes, decisions, blockers here -->

### Decisions Made
- **Name**: `bcq` (Basecamp Query, evokes jq/yq)
- **Language**: Bash (pragmatic start, clear upgrade path)
- **Config**: Layered (~/.config/basecamp + .basecamp/)
- **Output**: Auto-detect (JSON for pipes, Markdown for TTY)
- **Auth**: OAuth 2.1 with DCR (no manual token copy)

### Open Questions
- Best approach for mock server in tests? (Ruby WEBrick vs Python vs Node)
- Should we support `bcq todos | bcq done` pipeline for bulk ops?
- How to handle multi-account users in context?
