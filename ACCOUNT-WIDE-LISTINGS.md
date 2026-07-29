# Account-Wide Listings

Authoritative contract for exposing the SDK's `EverythingService` aggregates
(basecamp/basecamp-sdk#435, #438) through the CLI. This supersedes the "open
design question" framing in basecamp/basecamp-cli#585 and the "deliberately not
wired" note in PR #584.

## Shape

The aggregates are **not** a new command group. `Everything` is Basecamp web-UI
vocabulary; these are the account-wide variants of resources the CLI already
owns. Each aggregate lands on the group that owns its noun, reached by the
group's existing leaf list command.

## Method matrix

All 17 methods, and the invocation that reaches each.

| Command | SDK method | Payload | Paginated |
|---|---|---|---|
| `messages list --all-projects` | `Messages` | `.Recordings` | yes |
| `comments list --all-projects` | `Comments` | `.Recordings` | yes |
| `checkins answers --all-projects` | `Checkins` | `.Recordings` | yes |
| `forwards list --all-projects` | `Forwards` | `.Recordings` | yes |
| `boost list --all-projects` | `Boosts` | `.Boosts` | yes |
| `files list --all-projects` | `Files` | `.Files` | yes |
| `todos list --all-projects` | `OpenTodos` | `.Groups` | yes |
| `todos list --all-projects --status completed` | `CompletedTodos` | `.Groups` | yes |
| `todos list --all-projects --unassigned` | `UnassignedTodos` | `.Groups` | yes |
| `todos list --all-projects --no-due-date` | `NoDueDateTodos` | `.Groups` | yes |
| `todos list --all-projects --overdue` | `OverdueTodos` | flat `[]Todo` | **no** |
| `cards list --all-projects` | `OpenCards` | `.Groups` | yes |
| `cards list --all-projects --status completed` | `CompletedCards` | `.Groups` | yes |
| `cards list --all-projects --unassigned` | `UnassignedCards` | `.Groups` | yes |
| `cards list --all-projects --no-due-date` | `NoDueDateCards` | `.Groups` | yes |
| `cards list --all-projects --not-now` | `NotNowCards` | `.Groups` | yes |
| `cards list --all-projects --overdue` | `OverdueCards` | flat `[]Card` | **no** |

`reports overdue` is **not** replaced or deprecated. It is a lateness-bucketed
report (under a week / over a week / over a month / over three months);
`todos list --overdue` is a flat oldest-first aggregate. Both stay, and the
distinction is documented rather than resolved.

## Invariants

### I1 — Scope is selected before scope-specific validation

Determine the scope first, then validate against that scope. Validating a flag
under project rules before the account-wide branch is reached is a defect: the
paginated aggregates accept **any positive page**, while several project-scoped
commands only permit page 1.

Conversely `--page` must return `ErrUsage` against the **unpaginated** overdue
endpoints, which take no page argument at all.

### I2 — Scope precedence

Three inputs, in order:

1. **Explicit project** — `--project`, `-p`, or `--in`, accepted both at the
   root (`basecamp --project X todos list`, parsed in `internal/cli/root.go`
   into `app.Flags.Project`) and after the group noun. Detection must not rely
   on `cmd.Flags().Changed` alone, which misses the root-level form.
2. **Configured project** — `app.Config.ProjectID`.
3. **Nothing** — account-wide.

`--all-projects` **overrides** a configured project and **conflicts** with an
explicit one (`ErrUsage`). Without it, a user with a configured project could
never reach these listings, and identical scripts would change behavior with
ambient config.

For the per-item groups (`comments`, `boost`, `checkins answers`) the natural
trigger is an omitted positional ID; supplying both an ID and `--all-projects`
is a conflict.

### I3 — No flag is silently ignored

Every accepted flag must either affect the account-wide result or return an
actionable `ErrUsage`. This is the primary defect class.

**Scope-child flags** name a container inside one project and are meaningless
account-wide. Reject by name, including aliases:
`--message-board`, `--inbox`, `--vault`/`--folder`, `--card-table`, `--column`,
`--list`/`--todolist`, `--todoset`, `--questionnaire`, `--event`, `--by`.
A configured todolist is subject to the same rule as a configured project.

**Filters with no aggregate equivalent** (`--assignee`, unsupported `--status`
values) are rejected, pointing at the command that does answer the question
(e.g. `reports assigned`).

### I4 — Sorting

`--sort`/`--reverse` are **rejected for the grouped todo and card aggregates**.
The payload is nested by project, and raw grouped machine output cannot
represent a globally sorted cross-project list; sorting within each group is a
different contract from the existing global `--sort` and must not be passed off
as it.

They are **allowed for the flat results** (overdue todos/cards, and the
recording feeds) where a client-side sort helper exists or can be adapted.

Everywhere: `--reverse` without `--sort` is an error, and **sorting precedes
truncation** — truncating first would sort only the surviving window.

### I5 — Pagination and limits

The aggregates take a single page number: `0` follows every page, `N` returns
exactly page `N`.

| Flag | Mapping |
|---|---|
| `--all` | page 0 |
| `--page N` | page N (any positive N) |
| `--limit N` | client-side truncation to N, with an honest truncation notice |
| default | the command's documented default cap (commonly 100), via client-side truncation |

The default must remain the documented cap. Degrading it to "one raw SDK page"
is a silent contract change.

**Accepted tradeoff.** Honoring a 100-item cap by fetching page 0 downloads
every page before truncating, which is correct but potentially expensive on
large accounts. Where sorting is not in play, a bounded loop over positive pages
that stops once the limit is collected is the cheaper equivalent and is
preferred. Recorded here so the cost is a decision rather than an accident.

### I6 — Output contract

Project-scoped paths return concrete types (`[]Message`, `[]Todo`). Aggregate
paths return `[]Recording`, `[]EverythingFile`, or **nested** bucket groups.
Handing nested groups to the styled renderer produces unreadable nested cells.

Branch on `app.Output.EffectiveFormat()`, following the `humanizeSearchResults`
precedent in `internal/commands/search.go`:

- **machine formats** (`--json`, `--agent`, `--md`): raw SDK payload, grouping
  preserved.
- **styled**: flattened rows carrying at least project name, id, and
  title/subject, plus status and due date where applicable.

`EverythingFile` is an all-pointer superset over the Upload, Document, and
Attachment variants — every field read during flattening must be nil-checked.

### I7 — No interactive prompt

`ensureProject()` is unreachable on the account-wide path. The bare command
**group** still shows help; only the leaf list invocation lists account-wide.

## Behavior changes to release-note

- The interactive project prompt no longer fires on these list commands when no
  project is configured; they list account-wide instead.
- `todos list` with no project and `--overdue`/`--assignee` previously errored
  with a redirect. `--overdue` now returns results; `--assignee` still errors.
