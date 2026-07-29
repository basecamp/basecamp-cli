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

#### Per-item dispatch

The per-item groups (`comments`, `boost`, `checkins answers`) list the children
of one recording, so a project alone cannot produce a listing — only an item ID
can. Their dispatch is therefore its own truth table, and it overrides the
general precedence above:

| ID/URL | Explicit project | `--all-projects` | Result |
|---|---|---|---|
| present | any | absent | item-scoped |
| present | any | present | **ErrUsage** (conflict) |
| absent | present | present | **ErrUsage** (conflict) — the general I2 rule still applies |
| absent | present | absent | **ErrUsage** — ask for an ID |
| absent | absent (configured only) | absent | account-wide; ambient config ignored because it cannot scope this operation |
| absent | absent | present | account-wide, intent pinned |

The fourth row is the deliberate exception to "a configured project selects
project scope": for these three commands a configured project is not a scope
that can be honored, so it is ignored rather than turned into an error.

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

**No new flags** beyond `--all-projects`, the endpoint selectors the method
matrix names, and one recorded exception. The account-wide path reuses the
filter, sort, and pagination flags a command already has; it does not grow a
parallel flag surface. The exception is `files list`, which today has no filters
at all and gains two account-wide-only ones — see I5.

#### Endpoint selectors

The method matrix reaches eleven distinct todo/card endpoints, and the flags
that pick among them do not all exist yet. This is the complete inventory of
flags added by this work — anything not listed here is reuse:

| Command | New flag | Selects | Scope |
|---|---|---|---|
| all eight | `--all-projects` | account-wide | — |
| `todos list` | `--unassigned` | `UnassignedTodos` | account-wide only |
| `todos list` | `--no-due-date` | `NoDueDateTodos` | account-wide only |
| `cards list` | `--status` (only `completed`) | `CompletedCards` | account-wide only |
| `cards list` | `--unassigned` | `UnassignedCards` | account-wide only |
| `cards list` | `--no-due-date` | `NoDueDateCards` | account-wide only |
| `cards list` | `--not-now` | `NotNowCards` | account-wide only |
| `cards list` | `--overdue` | `OverdueCards` | account-wide only |
| `files list` | `--kind`, `--person` | filters on `Files` | account-wide only — see I5 |

**Account-wide only** means exactly what it means for the `files list` filters:
the project-scoped path has no equivalent, so passing one with a project in
scope is `ErrUsage`, not a silent no-op. `todos list --status completed`,
`--completed`, and `--overdue` already exist project-scoped and keep working
there; they merely gain an account-wide meaning.

The selectors are **mutually exclusive** — each picks one endpoint, and there is
no endpoint that combines two. Passing more than one is `ErrUsage` naming the
pair.

### I4 — Sorting

**Sorting flags are reused where they exist and never added.** Verified current
state: among these commands only `messages list`, `todos list`, and `cards list`
expose `--sort`/`--reverse`. `comments list`, `checkins answers`,
`forwards list`, `boost list`, and bare `files list` expose neither and gain
neither — an unsorted account-wide feed is the honest result, not a gap to
paper over.

`--sort`/`--reverse` are **rejected for the grouped todo and card aggregates**.
The payload is nested by project, and raw grouped machine output cannot
represent a globally sorted cross-project list; sorting within each group is a
different contract from the existing global `--sort` and must not be passed off
as it.

They are **allowed for the flat results** — overdue todos/cards (flat `[]Todo`,
`[]Card`) and `messages list --all-projects` (flat `[]Recording`) — where a
client-side sort helper exists or can be adapted.

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
| default | the command's documented default, per the table below |

#### Per-command defaults

"The command's documented default" is not left to interpretation. Each
command's account-wide default matches what it already does project-scoped:

| Command | Existing default | Account-wide contract |
|---|---|---|
| `messages list` | 100 | cap 100 |
| `comments list` | 100 | cap 100 |
| `checkins answers` | all | all |
| `forwards list` | all | all |
| `boost list` | no paging flags | first page only |
| `files list` | no paging flags | all pages |
| `todos list` | 100 | cap 100 |
| `cards list` | all | all |

The default must remain the documented default. Degrading a documented cap to
"one raw SDK page" is a silent contract change; so is promoting an unpaged
command to a capped one.

#### Rules

- **No new pagination flags where none exist.** `boost list` and bare
  `files list` have no `--limit`/`--page`/`--all` today and gain none. Their
  account-wide behavior is fixed by the table above.
- **`--limit N` on grouped todo/card responses counts inner todos/cards, not
  outer project groups.** Truncating groups would silently drop whole projects.
- **`Meta.TotalCount` counts groups, not items,** for the grouped responses.
  `accountWideRespOpts` therefore must **not** be used for grouped item-count
  notices — the command computes its own item total and builds its own summary.
- **Explicit `--page 0`, a negative `--page`, and a negative `--limit` return
  `ErrUsage`.** `--page 0` means "unset" to Cobra's default but "follow every
  page" to the SDK; accepting it silently would hand the user a full-account
  crawl they did not ask for, and that is the same no-silent-flags defect I3
  names. `--all` is the spelling for every page.

#### The `files list` filter exception

`EverythingFilesOptions` carries filters the project-scoped path has no
equivalent for: `Vaults`, `Uploads`, and `Documents` `List()` take neither a
kind nor a creator. Rather than leave adopted SDK surface unreachable, bare
`files list` gains two flags:

| Flag | Value | Maps to |
|---|---|---|
| `--kind` | `all`, `images`, `pdfs`, `documents`, `videos` | `EverythingFilesOptions.Kind` |
| `--person` | repeatable; name, email, ID, or `me`, resolved via `resolveAssigneeID` | `EverythingFilesOptions.PeopleIDs` |

Both are **account-wide-only**. Passing either with a project in scope —
explicit, configured, or via `--vault`/`--folder` — is `ErrUsage`, because the
project-scoped path would have to ignore them, and I3 forbids that. An
unrecognized `--kind` value is `ErrUsage` listing the accepted set.

This is the one deliberate exception to I3's "no new flags"; it is recorded here
so it stays an exception rather than a precedent.

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

Which payloads actually need flattening, so the eight commands do not each
answer this differently:

| Payload | Commands | Styled treatment |
|---|---|---|
| `[]Recording` | messages, comments, checkins answers, forwards | flatten — the generic renderer drops the nested `bucket`, and a project column is exactly what an account-wide row needs |
| `[]Todo`, `[]Card` (flat overdue) | todos, cards `--overdue` | rendered as-is, like the project-scoped path |
| `[]EverythingBoost` | boost | flatten — the boosted `*Recording` is nested |
| `[]EverythingFile` | files | flatten — all-pointer superset, too wide to render raw |
| `[]BucketTodosGroup`, `[]BucketCardsGroup` | todos, cards | flatten — nested groups render as unreadable cells |

`recordings list` hands `[]Recording` straight to the renderer, and that is fine
for it: it is already project-scoped, so the missing project column costs
nothing. It is not a precedent for the aggregates.

**Mechanism.** Flattening is supplied through `output.WithDisplayData`, not by
branching on `EffectiveFormat()`. `Data` stays the raw SDK payload so `--json`
and `--agent` keep it; `DisplayData` carries the flat rows that styled,
`--md`, `--ids`, and `--count` all read. Branching on the format instead leaves
the other three reading the nested payload — where `--count` counts project
groups and `--ids` finds no ids at all, both silently.

`EverythingFile` is an all-pointer superset over the Upload, Document, and
Attachment variants — every field read during flattening must be nil-checked.
`EverythingBoost.Booster` and `.Recording` are pointers too.

### I7 — No interactive prompt

`ensureProject()` is unreachable on the account-wide path. The bare command
**group** still shows help; only the leaf list invocation lists account-wide.

## Behavior changes to release-note

- The interactive project prompt no longer fires on these list commands when no
  project is configured; they list account-wide instead.
- `todos list` with no project and `--overdue`/`--assignee` previously errored
  with a redirect. `--overdue` now returns results; `--assignee` still errors.
