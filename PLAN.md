# Plan: Interactive Resolution for CLI Options

## Vision

Transform bcq from a flags-required CLI into a conversational tool that guides users through missing inputs. When required options aren't specified and the terminal is interactive, show beautiful TUI pickers instead of error messages.

**Before:** `bcq todos` → `Error: --project is required`
**After:** `bcq todos` → Shows project picker → Shows todolist picker → Lists todos

---

## Current State

✅ **Account resolution** - Implemented in `elicit` branch
- `ensureAccount()` pattern in projects command
- `resolve.Account()` with picker fallback
- Persist option to save selection to config

---

## Opportunities

### Tier 0: Environment/Context

| Resource | Commands Affected | Dependencies | Notes |
|----------|------------------|--------------|-------|
| **Host** | All commands | None | Pick production/beta/dev from configured hosts |
| **Account** | All account-scoped commands | Host | ✅ Done |

### Tier 1: High-Value, Common Operations

| Resource | Commands Affected | Dependencies | Notes |
|----------|------------------|--------------|-------|
| **Project** | todos, messages, schedules, documents, cards, campfires, webhooks | Account | Most commands need this |
| **Todolist** | todos (list, create) | Account → Project | Second-most common |
| **Person** | todos (assign), mentions, access control | Account (→ Project optional) | For `--assignee`, `--notify` flags |

### Tier 2: Medium-Value

| Resource | Commands Affected | Dependencies | Notes |
|----------|------------------|--------------|-------|
| **Message Board** | messages | Account → Project | Auto-resolve from dock |
| **Schedule** | schedule entries | Account → Project | Auto-resolve from dock |
| **Vault** | documents, uploads | Account → Project | Auto-resolve from dock |
| **Card Table** | cards | Account → Project | If multiple tables exist |

### Tier 3: Deep Hierarchy (Dock Tools & Children)

| Resource | Commands Affected | Dependencies | Notes |
|----------|------------------|--------------|-------|
| **Campfire** | campfire lines | Account → Project → Campfire | Usually one per project |
| **Campfire Line** | campfire reply | Account → Project → Campfire → Line | Pick which message to reply to |
| **Todo** | todos complete, show, update | Account → Project → Todolist → Todo | Pick which todo to act on |
| **Message** | messages show, comment | Account → Project → Message Board → Message | Pick which message |
| **Document** | documents show, update | Account → Project → Vault → Document | Pick which document |
| **Schedule Entry** | schedule show, update | Account → Project → Schedule → Entry | Pick which entry |
| **Card** | cards show, move | Account → Project → Card Table → Column → Card | Full card hierarchy |
| **Template** | projects create | Account | For `--template` flag |

### Full Hierarchy Tree

```
Host
└── Account
    └── Project
        ├── Message Board (dock)
        │   └── Message
        │       └── Comment
        ├── Todolist (dock: todoset)
        │   └── Todo
        │       └── Comment
        ├── Schedule (dock)
        │   └── Schedule Entry
        │       └── Comment
        ├── Vault (dock)
        │   └── Document/Upload
        │       └── Comment
        ├── Card Table (dock)
        │   └── Column
        │       └── Card
        │           └── Comment
        ├── Campfire (dock)
        │   └── Campfire Line
        └── Questionnaire (dock: questions)
            └── Question
                └── Answer
```

---

## Architecture

### Resolver Methods

```go
// Tier 0 - Environment
func (r *Resolver) Host(ctx) (*ResolvedValue, error)      // Pick from configured hosts
func (r *Resolver) Account(ctx) (*ResolvedValue, error)   // ✅ Done

// Tier 1 - Common resources
func (r *Resolver) Project(ctx) (*ResolvedValue, error)   // Needs account
func (r *Resolver) Todolist(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) Person(ctx, projectID) (*ResolvedValue, error)  // Project-scoped people
func (r *Resolver) PersonGlobal(ctx) (*ResolvedValue, error)       // All people in account

// Tier 2 - Dock-based (auto-resolve from dock, picker if multiple)
func (r *Resolver) MessageBoard(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) Schedule(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) Vault(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) CardTable(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) Campfire(ctx, projectID) (*ResolvedValue, error)
func (r *Resolver) Questionnaire(ctx, projectID) (*ResolvedValue, error)

// Tier 3 - Deep hierarchy (children of dock tools)
func (r *Resolver) Todo(ctx, projectID, todolistID) (*ResolvedValue, error)
func (r *Resolver) Message(ctx, projectID, boardID) (*ResolvedValue, error)
func (r *Resolver) Document(ctx, projectID, vaultID) (*ResolvedValue, error)
func (r *Resolver) ScheduleEntry(ctx, projectID, scheduleID) (*ResolvedValue, error)
func (r *Resolver) Card(ctx, projectID, tableID) (*ResolvedValue, error)        // Returns card, also resolves column
func (r *Resolver) CardColumn(ctx, projectID, tableID) (*ResolvedValue, error)  // Just column
func (r *Resolver) CampfireLine(ctx, projectID, campfireID) (*ResolvedValue, error)
func (r *Resolver) Question(ctx, projectID, questionnaireID) (*ResolvedValue, error)
func (r *Resolver) Answer(ctx, projectID, questionID) (*ResolvedValue, error)

// Tier 4 - Comments (generic across all commentable resources)
func (r *Resolver) Comment(ctx, recordingURL) (*ResolvedValue, error)  // Works with any recording
```

### Host Resolution

Hosts are configured in bcq config for different environments:

```yaml
# ~/.config/bcq/config.yaml
hosts:
  production:
    base_url: https://3.basecampapi.com
    client_id: abc123
  beta:
    base_url: https://3.basecamp-beta.com
    client_id: def456
  dev:
    base_url: http://localhost:3000
    client_id: local
```

Resolution flow:
1. Check `--host` flag
2. Check `BCQ_HOST` env var
3. Check config `default_host`
4. If multiple hosts configured → show picker
5. If single host → use it as default

### Person Resolution

Two modes:
- **Project-scoped**: People with access to specific project (for assignments)
- **Account-scoped**: All people in account (for admin operations)

```go
// Project-scoped - for todo assignments
people, _ := app.Account().People().ListProjectPeople(ctx, projectID)

// Account-scoped - all people
people, _ := app.Account().People().List(ctx)
```

Picker enhancements for people:
- Show avatar/initials
- Show role (admin, owner, member)
- Fuzzy search by name
- Show email for disambiguation

### Command Integration Pattern

```go
func runTodosList(cmd *cobra.Command, args []string) error {
    app := appctx.FromContext(cmd.Context())

    // Resolve chain: Account → Project → Todolist
    if err := ensureAccount(cmd, app); err != nil {
        return err
    }
    if err := ensureProject(cmd, app); err != nil {
        return err
    }
    if err := ensureTodolist(cmd, app); err != nil {
        return err
    }

    // Now all IDs are available
    todos, err := app.Account().Todos().List(ctx, projectID, todolistID, opts)
    ...
}
```

### Dock Auto-Resolution

Many tools (message board, schedule, vault) are 1:1 with projects. Instead of prompting, auto-resolve from the project's dock:

```go
func (r *Resolver) MessageBoard(ctx, projectID) (*ResolvedValue, error) {
    project, _ := r.sdk.ForAccount(accountID).Projects().Get(ctx, projectID)
    for _, dock := range project.Dock {
        if dock.Name == "message_board" && dock.Enabled {
            return &ResolvedValue{Value: dock.ID, Source: SourceDefault}, nil
        }
    }
    return nil, errors.New("no message board enabled for this project")
}
```

---

## Implementation Order

### Phase 0: Picker Infrastructure
1. Add loading state with spinner to picker
2. Implement paginated picker model
3. Add progressive loading (fetch next page near bottom)
4. Add pre-fetch for smoother UX
5. Test with slow network simulation

### Phase 1: Host Resolution
1. Add `resolve.Host()` with picker from configured hosts
2. Add `ensureHost()` helper at app initialization
3. Update SDK client creation to use resolved host
4. Add `--host` flag to root command

### Phase 2: Project Resolution
1. Add `resolve.Project()` with paginated picker
2. Add `ensureProject()` helper
3. Integrate into: todos, messages, schedules, documents, cards, webhooks
4. Add `--project` persistence option
5. Prioritize bookmarked projects in picker (show ★)
6. Show project status (active/archived)

### Phase 3: Todolist Resolution
1. Add `resolve.Todolist()` with paginated picker
2. Add `ensureTodolist()` helper
3. Integrate into todos command
4. Show todo count per todolist
5. Cache todolist list for performance

### Phase 4: Person Resolution
1. Add `resolve.Person()` for assignee selection
2. Support both project-scoped and account-scoped
3. Integrate into todos create/update (`--assignee`)
4. Fuzzy search by name with email disambiguation
5. Show role badges (admin, owner)

### Phase 5: Dock Auto-Resolution
1. Add dock-based resolution for message board, schedule, vault, card table
2. Cache project dock info to avoid repeated fetches
3. Fall back to picker if multiple enabled (rare)

### Phase 6: Deep Hierarchy
1. **Todos**: Project → Todolist → Todo picker for `todos complete <partial>`
2. **Messages**: Project → Message Board → Message picker for `messages show`
3. **Documents**: Project → Vault → Document picker
4. **Cards**: Project → Card Table → Column → Card picker
5. **Schedule**: Project → Schedule → Entry picker
6. **Campfire**: Project → Campfire → Line picker for replies

### Phase 7: Comments (all resource types)
1. Generic comment picker for any parent resource
2. Support `bcq comment <url>` to reply to specific comment
3. Thread display in picker

---

## UX Considerations

### Loading States

Show beautiful loading feedback while fetching options:

```
Select a project
  ⠋ Loading projects...
```

Spinner styles (using lipgloss):
- Dots: `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`
- Progress indication for known counts
- Subtle color (muted foreground)

Implementation:
```go
type PickerModel struct {
    loading     bool
    loadingMsg  string
    spinner     spinner.Model
    items       []PickerItem
    // ...
}

func (m PickerModel) View() string {
    if m.loading {
        return m.spinner.View() + " " + m.loadingMsg
    }
    // ... render items
}
```

### Progressive Pagination

Don't wait for all results - show first page immediately, load more as user scrolls:

```
Select a project
  Type to filter...

> ◆ Project Alpha           #123
  ○ Project Beta            #456
  ○ Project Gamma           #789
  ○ Project Delta           #012
  ○ Project Epsilon         #345
  ─────────────────────────────
  ↓ Loading more... (5 of 47)
```

Implementation:
```go
type PaginatedPicker struct {
    items       []PickerItem      // Currently loaded items
    cursor      int               // Current selection
    offset      int               // Scroll offset
    hasMore     bool              // More pages available
    loading     bool              // Currently fetching next page
    nextCursor  string            // Pagination cursor from API
}

func (m *PaginatedPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        if msg.String() == "down" && m.cursor >= len(m.items)-3 && m.hasMore && !m.loading {
            // Near bottom, trigger fetch
            return m, m.fetchNextPage()
        }
    case itemsLoadedMsg:
        m.items = append(m.items, msg.items...)
        m.hasMore = msg.hasMore
        m.nextCursor = msg.nextCursor
        m.loading = false
    }
}
```

API integration:
- Use SDK pagination (Link headers, cursors)
- Fetch 25-50 items per page
- Pre-fetch next page when user is 3 items from bottom
- Cancel pending fetch if user selects or cancels

### Picker Enhancements
- **Fuzzy search** - Filter as you type (already have basic filter)
- **Recent items** - Show recently used projects/todolists first
- **Bookmarked** - Prioritize bookmarked projects (show ★ indicator)
- **Hierarchy display** - Show project → todolist relationship
- **Status indicators** - Show active/archived/trashed for projects
- **Counts** - Show "12 todos" next to todolist name

### Persistence Prompts
After interactive selection, offer to save:
- "Set as default project? [y/N]"
- Scope: global config vs local (.bcq.json in cwd)

### Non-Interactive Fallback
When not a TTY:
- Clear error message listing what's missing
- Hint showing the flag to use
- Example command with flag

---

## Files to Modify

### Picker Infrastructure
| File | Changes |
|------|---------|
| `internal/tui/picker.go` | Add loading state, spinner, pagination |
| `internal/tui/paginated_picker.go` | New - Paginated picker with progressive loading |
| `internal/tui/spinner.go` | New - Reusable spinner component |

### Resolution
| File | Changes |
|------|---------|
| `internal/tui/resolve/host.go` | New - Host/environment resolution |
| `internal/tui/resolve/project.go` | New - Project resolution with pagination |
| `internal/tui/resolve/todolist.go` | New - Todolist resolution |
| `internal/tui/resolve/todo.go` | New - Individual todo resolution |
| `internal/tui/resolve/person.go` | New - Person resolution |
| `internal/tui/resolve/dock.go` | New - Dock auto-resolution helpers |
| `internal/tui/resolve/message.go` | New - Message resolution |
| `internal/tui/resolve/document.go` | New - Document resolution |
| `internal/tui/resolve/card.go` | New - Card hierarchy resolution |
| `internal/tui/resolve/schedule.go` | New - Schedule entry resolution |
| `internal/tui/resolve/campfire.go` | New - Campfire/line resolution |

### Config & Context
| File | Changes |
|------|---------|
| `internal/config/config.go` | Add hosts map, default_host |
| `internal/appctx/context.go` | Host resolution at startup |

### Commands
| File | Changes |
|------|---------|
| `internal/commands/root.go` | Add `--host` flag |
| `internal/commands/todos.go` | Add ensureProject, ensureTodolist, todo picker, person for --assignee |
| `internal/commands/messages.go` | Add ensureProject, message picker |
| `internal/commands/schedules.go` | Add ensureProject, entry picker |
| `internal/commands/documents.go` | Add ensureProject, document picker |
| `internal/commands/cards.go` | Add ensureProject, card hierarchy picker |
| `internal/commands/webhooks.go` | Add ensureProject |
| `internal/commands/people.go` | Person listing for picker data |
| `internal/commands/campfires.go` | Add campfire/line picker |

---

## Resolution Chain

Full chain for a command like `bcq todos create`:

```
Host → Account → Project → Todolist → [Person for --assignee]
```

Each step:
1. Check flag
2. Check env var
3. Check config
4. Interactive prompt (if TTY)
5. Error (if non-TTY and required)

### Short-Circuit Optimizations

- If `--project 123` provided, skip project picker
- If `--todolist 456` provided, skip todolist picker
- If URL context available (from recent `bcq url parse`), pre-fill project/todolist
- If only one option exists (single project, single todolist), auto-select

---

## Open Questions

1. **Caching** - Should we cache project/todolist lists for faster repeat prompts?
2. **Default project per directory** - Support `.bcq.json` in cwd for project-specific defaults?
3. **URL context** - If user ran `bcq url parse <url>` recently, use that project as default?
4. **Multi-select** - Any use cases for selecting multiple items? (e.g., batch todo complete)
5. **Host switching** - Should `bcq host switch` be a command, or always via flag/env?
6. **Person caching** - Cache people list per account? How long?
7. **Offline mode** - What to show when can't fetch options? Use cached data?
