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

All 16 methods, and the invocation that reaches each. The family was 17 through
SDK v0.10.0; `Boosts` was removed in v0.11.0, for the reasons under
"`boost list` — withdrawn" below.

| Command | SDK method | Payload | Paginated |
|---|---|---|---|
| `messages list --all-projects` | `Messages` | `.Recordings` | yes |
| `comments list --all-projects` | `Comments` | `.Recordings` | yes |
| `checkins answers --all-projects` | `Checkins` | `.Recordings` | yes |
| `forwards list --all-projects` | `Forwards` | `.Recordings` | yes |
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

The per-item groups (`comments`, `checkins answers`) list the children of one
recording, so a project alone cannot produce a listing — only an item ID can.
Their dispatch is therefore its own truth table, and it overrides the general
precedence above:

| ID/URL | Explicit project | `--all-projects` | Result |
|---|---|---|---|
| present | any | absent | item-scoped |
| present | any | present | **ErrUsage** (conflict) |
| absent | present | present | **ErrUsage** (conflict) — the general I2 rule still applies |
| absent | present | absent | **ErrUsage** — ask for an ID |
| absent | absent (configured only) | absent | account-wide; ambient config ignored because it cannot scope this operation |
| absent | absent | present | account-wide, intent pinned |

The fourth row is the deliberate exception to "a configured project selects
project scope": for these two commands a configured project is not a scope that
can be honored, so it is ignored rather than turned into an error.

`boost list` is per-item in the same way but has **no account-wide row at all**
— the aggregate behind it was withdrawn server-side. Every absent-ID case
asks for an ID, and it carries no `--all-projects`. See "`boost list` —
withdrawn" under I5.

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
matrix names, the `files list` filters, and the pagination flags the two
flagless commands needed. The account-wide path reuses the filter and sort flags
a command already has; it does not grow a parallel flag surface for its own
sake.

Pagination is the one place where "reuse only" turned out to be wrong. A command
with no `--limit`/`--page`/`--all` has no way to recover from a server error
mid-crawl and no way to reach past a bounded default, so bare `files list`
gained all three — see I5. (`boost list` gained them too, then lost them along
with the account-wide boost feed itself — see below.)

#### Endpoint selectors

The method matrix reaches eleven distinct todo/card endpoints, and the flags
that pick among them do not all exist yet. This is the complete inventory of
flags added by this work — anything not listed here is reuse:

| Command | New flag | Selects | Scope |
|---|---|---|---|
| all seven | `--all-projects` | account-wide | — |
| `todos list` | `--unassigned` | `UnassignedTodos` | account-wide only |
| `todos list` | `--no-due-date` | `NoDueDateTodos` | account-wide only |
| `cards list` | `--status` (only `completed`) | `CompletedCards` | account-wide only |
| `cards list` | `--unassigned` | `UnassignedCards` | account-wide only |
| `cards list` | `--no-due-date` | `NoDueDateCards` | account-wide only |
| `cards list` | `--not-now` | `NotNowCards` | account-wide only |
| `cards list` | `--overdue` | `OverdueCards` | account-wide only |
| `files list` | `--kind`, `--person` | filters on `Files` | account-wide only — see I5 |
| `files list` | `--limit`/`-n`, `--page`, `--all` | pagination on `Files` | account-wide only — see I5 |

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
`forwards list`, and bare `files list` expose neither and gain
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
| `--all` | page 0 — the whole account |
| `--page N` | page N (any positive N) |
| `--limit N` | **bounds the fetch**: walks positive pages until N items are collected, then trims to exactly N |
| default | a bounded cap, per the table below |

`--limit` bounding the fetch is the point. Fetching page 0 and then truncating
is correct and useless: it downloads the entire account to keep the first
hundred rows, which is what made `cards list --limit 3` take 16s while
`todos list --limit 3` took none. `accountWideCollect` is the shared walk.

**Three deliberate exceptions**, each with a reason that is not "we didn't get
to it":

1. **Sorted `messages list`** fetches every page before capping. The cap applies
   after the sort (I4), so every page has to be in hand first. Pinned by
   `TestMessagesListAccountWideSortsBeforeTruncating`.
2. **The overdue endpoints** (`todos list --overdue`, `cards list --overdue`)
   are unpaginated — one request returns everything — so `--limit` trims
   locally. There is no walk to bound.
3. ~~**`boost list`** keeps a first-page default.~~ Withdrawn — there is no
   account-wide boost listing at all now. See below.

#### Per-command defaults

Pinning each account-wide default to its project-scoped default was a mistake,
and it is the mistake this section exists to correct. Project-scoped "all" is
one project's items; account-wide "all" is the whole account. The same word
describes two very different amounts of work, and on a ~80-project account the
account-wide reading of it timed out.

**The default is a uniform cap of 100** (`accountWideDefaultLimit`), with two
rows that differ for stated reasons. `--all` is how you ask for the account.

| Command | Account-wide default | Notes |
|---|---|---|
| `messages list` | cap 100 | unchanged |
| `comments list` | cap 100 | unchanged |
| `todos list` | cap 100 | unchanged |
| `cards list` | cap 100 | **changed** from "all pages" |
| `checkins answers` | cap 100 | **changed** from "all pages" |
| `forwards list` | cap 100 | **changed** from "all pages" |
| `files list` | cap 100 | **changed** from "all pages" |
| `todos list --overdue` | cap 100 | unpaginated endpoint; accepts `--all`, rejects `--page` |
| `cards list --overdue` | cap 100 | **changed** from uncapped; same rules as above |

Project-scoped defaults are untouched throughout.

**The two overdue rows.** Both endpoints are unpaginated, so `--page` has
nothing to address and stays an error. `--all` is a different question: it means
"skip the cap", and since the complete array is already in hand it costs no
extra request. Previously `todos --overdue` capped at 100 *and* rejected `--all`,
which left item 101 unreachable by any flag combination — capped with no escape
hatch. `cards --overdue` was uncapped and also rejected `--all`.

**Changing a default is allowed when the old one was wrong.** The earlier
version of this section forbade moving a default in either direction. That rule
protected a contract that could not be honored at scale. What must not happen is
a default changing *silently*: each row marked **changed** above is a documented
behavior change and belongs in the release notes.

#### Rules

- **Every account-wide listing needs a bounded default and a way to ask for
  more.** A listing with no escape hatch cannot recover from a server error
  mid-crawl: when `files list --all-projects` returned a 500 partway through the
  full-account crawl, there was no flag that could step around the failing page,
  because the command had none. A bounded default without a way past it is the
  same defect from the other side.
- **A registered flag must work on every path that accepts it.** Where a path
  genuinely has no pagination, it rejects all three by name rather than
  accepting and ignoring them (`rejectScopedPaginationFlags`):
  - **Project-scoped `files list`** rejects `--limit`/`--page`/`--all`. That
    path passes `nil` to `Vaults().List`, `Uploads().List`, and
    `Documents().List` — three unpaginated calls with nowhere to put a page.
  - **`boost list`** carries none of the three at all. The SDK documents
    `BoostListOptions.Page` as not honoring the page number — setting `Page=2`
    does not fetch page 2 — and the account-wide feed they were added for is
    gone.
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

#### `boost list` — withdrawn

There is **no account-wide boost listing.** `boost list` requires an item ID.

The `/boosts.json` aggregate this section used to document was an easter egg —
unlinked from the Basecamp web UI, ~2,250 requests per 30 days globally, and
this CLI its only known consumer. BC5 **withdrew it** (bc3#12464), and the path
now 404s on both the web and API hosts, so the CLI no longer calls it. The SDK
dropped `Everything().Boosts()` to match in v0.11.0 (basecamp/basecamp-sdk#504),
so the operation is not merely unused here — it no longer exists.

The cause, from BC3's own diagnosis rather than inference: the query's cost is
proportional to the account's **accessible recordings, not its boosts**. That
is why it measured ~44s on the largest account and why page 40 cost the same as
page 1 (bc3#12458 has the timings).

**The withdrawal is temporary.** The feed is expected back on a
boost-proportional query, via a `boosts.bucket_id` denormalization, with the
design record in bc3#12463. Public notice: basecamp/bc-api#427. So this section
describes a listing that is gone for now, not one that was judged a bad idea.

What that means here:

- `boost list` takes an item ID, and rejecting a bare invocation is the honest
  answer rather than a fallback to an endpoint that will 404.
- It carries no `--all-projects`, `--limit`, `--page`, or `--all`. An item's
  boosts arrive in one unpaginated response, and the SDK documents
  `BoostListOptions.Page` as not honoring a page number, so there would be
  nothing for those flags to address even if the aggregate had survived.
- **`Everything().Boosts()` is being removed from the SDK too.** The initial
  brief asked that it stay — churning every generated client twice for a
  temporary withdrawal seemed worse than letting it 404 — but the endpoint is
  going away for real on the server, so the SDK drops it rather than ship an
  operation that cannot work. That is the SDK's call; this repo only has to not
  call it, which it already does. The CLI carries **zero** references to the
  aggregate, so the SDK bump that removes it needs no CLI change.

Everything else is untouched: the other Everything feeds, the bucket-scoped
boosts endpoints, and the `boosts_count`/`boosts_url` recording attributes.

Seven commands list account-wide, not eight — until the feed returns, at which
point this is the section to revisit.

#### The `files list` filter exception

`EverythingFilesOptions` carries filters the project-scoped path has no
equivalent for: `Vaults`, `Uploads`, and `Documents` `List()` take neither a
kind nor a creator. Rather than leave adopted SDK surface unreachable, bare
`files list` gains two flags:

| Flag | Value | Maps to |
|---|---|---|
| `--kind` | `all`, `images`, `pdfs`, `documents`, `videos` | `EverythingFilesOptions.Kind` |
| `--person` | repeatable; name, email, ID, or `me`, resolved via `resolvePersonRoleID(ctx, app, person, "Person")` | `EverythingFilesOptions.PeopleIDs` |

Both are **account-wide-only**. Passing either with a project in scope —
explicit, configured, or via `--vault`/`--folder` — is `ErrUsage`, because the
project-scoped path would have to ignore them, and I3 forbids that. An
unrecognized `--kind` value is `ErrUsage` listing the accepted set.

These filters are account-wide-only by nature rather than by policy: the
project-scoped path has nothing to map them onto.

#### The `files` group's alias spellings

`vaults` (aliases `vault`, `folders`) and `docs` (alias `documents`) are
`NewFilesCmd` under different names, so they share this leaf. The account-wide feed
is not the same listing the project-scoped path returns: it carries Uploads,
Documents, and Attachments, and **no folder variant at all**.

Sharing the leaf unchanged would make `folders list --all-projects` return a
listing containing none of the thing the command is named for. Each spelling
therefore gets its own account-wide meaning:

| Spelling | Account-wide behavior |
|---|---|
| `files list` | the feed, `--kind` free, bounded by the cap and its pagination flags |
| `vaults` / `folders list` | **ErrUsage** — folders have no account-wide listing; points at the project-scoped form and at `files list --all-projects` |
| `docs` / `documents list` | the feed pinned to `--kind documents`; an explicit `--kind` is ErrUsage, since the command name already chose |

The project-scoped behavior of all three is unchanged.

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

Which payloads actually need flattening, so the seven commands do not each
answer this differently:

| Payload | Commands | Styled treatment |
|---|---|---|
| `[]Recording` | messages, comments, checkins answers, forwards | flatten — the generic renderer drops the nested `bucket`, and a project column is exactly what an account-wide row needs |
| `[]Todo`, `[]Card` (flat overdue) | todos, cards `--overdue` | flatten — same reason as `[]Recording`; the items come from every project and `bucket` is skipped by name |
| `[]EverythingFile` | files | flatten — all-pointer superset, too wide to render raw |
| `[]BucketTodosGroup`, `[]BucketCardsGroup` | todos, cards | flatten — nested groups render as unreadable cells |

**Every** account-wide payload flattens. The rule is not about nesting depth —
it is that `internal/output/render.go` skips `bucket` by name in both generic
renderers, so any aggregate handed over raw loses the one column that makes a
cross-project row attributable. "Renders fine project-scoped" is never the test:
project-scoped output needs no project column. `recordings list` hands
`[]Recording` over raw for exactly that reason, and it is not a precedent here.

The same goes for `WithEntity`. A schema that renders a task list has no column
for a project, so the account-wide overdue todo listing drops the entity rather
than lose the attribution.

**Mechanism.** Flattening is supplied through `output.WithDisplayData`, not by
branching on `EffectiveFormat()`. `Data` stays the raw SDK payload so `--json`
and `--agent` keep it; `DisplayData` carries the flat rows that styled,
`--md`, `--ids`, and `--count` all read. Branching on the format instead leaves
the other three reading the nested payload — where `--count` counts project
groups and `--ids` finds no ids at all, both silently.

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
