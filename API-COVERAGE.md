# Basecamp CLI API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 56 | 216 |
| ⚠️ Blocked | 0 | 0 |
| ⏭️ Out of scope | 4 | 12 |
| **Total tracked** | **60** | **228** |

**216 of 216 tracked in-scope endpoints.** Moving and copying a recording
into another project (`POST /recordings/:id/moves.json`,
`POST /recordings/:id/copies.json`) and reading the background filing they
start (`GET /filings/:id.json`), modelled by
[basecamp/basecamp-sdk#1053](https://github.com/basecamp/basecamp-sdk/pull/1053),
land for cards as `cards move --to-project`/`--to-card-table` and `cards copy`, and for
to-dos, to-do lists and groups as `todos move|copy`, `todolists move|copy` and
`todolistgroups move|copy` with `--to-project`/`--to-list`/`--to-todoset`. Uploading and removing a project's
wallpaper (`PUT`/`DELETE /projects/:id/wallpaper.json`, added by
[bc3#14062](https://github.com/basecamp/bc3/pull/14062) and shipped in
[SDK v0.28.0](https://github.com/basecamp/basecamp-sdk/releases/tag/v0.28.0))
land as `projects wallpaper`. Spotlighting a recording on its
project or template home page (`POST`/`DELETE /recordings/:id/spotlight.json`,
SDK v0.27.0) lands as `spotlight` and `unspotlight`. The two embed endpoints bc3 documents
in `embeds.md` — `POST /embeds.json` and `GET /embeds/:sgid.json` — land as
`embeds`. The three Pings endpoints bc3#13943
documented — `GET /circles.json`, `GET /circles/:id.json` and `POST
/circles.json` — land as `pings`. Moving a document, upload or folder
between folders (`POST /recordings/:id/filing.json`, documented by bc3#13644 and
modelled by basecamp/basecamp-sdk#960) lands as `files move`. The eight subtask endpoints bc3
#12659 documented — the flat `/recordings/:id/subtasks.json` and
`/subtasks/:id` routes, modelled by basecamp/basecamp-sdk#883 — land as
`subtasks`. The client-admission endpoints
basecamp/bc3#13098 added — `PUT /projects/:id/people/client_users.json` and
`POST`/`DELETE /projects/:id/client_enablement.json` — land as `people clients`.
SDK v0.16.0 adds the three to-do
list template-library operations, available through `templates library`,
`templates copy`, and `templates copy-status`. The previous last gap — `GET
/uploads/:id/versions.json` — closed with the v0.14.0 SDK bump. The command
(`files versions`) was written earlier but held: the SDK's
`UploadsService.ListVersions` decoded the response as `[]Upload` when the API
returns version *events*, so shipping it would have meant shipping wrong data.
basecamp/basecamp-sdk#683 (v0.14.0) returns a typed version, and the command
ships — see the `uploads` row.

An earlier revision of this file read "100% coverage of tracked in-scope API
(184/184)" while that gap was open. That was wrong, and the matrix had no way
to say so — with only ✅ and ⏭️ available, a partly-covered section had to be
recorded as fully covered. Hence the third status above, currently marking
nothing. It is deliberately narrow: ⚠️ means the CLI cannot faithfully cover an
endpoint for a reason outside the CLI, and it names the blocker.

This is not a complete bc-api parity figure. The five BC5 sections introduced by bc-api#410
that were previously untracked — `my_bookmarks`, `drafts`, `my_notes`,
`calendars`, and `question_reminders` — are now tracked and implemented. The
pinned SDK's `EverythingService` is fully reached — see [Account-wide
aggregates](#account-wide-aggregates).

Two corrections rode along with that count. The `questions` row claimed 5
endpoints while listing four actions, and the section carries pause, resume,
notification settings, and answerers besides — so the row was undercounting the
very section the 100%-of-tracked claim rests on. It now reads 8. And
`card_table_columns` gained `subscribe`/`unsubscribe` operations in the SDK that
the CLI deliberately does not spell twice; see that row.

Out-of-scope sections are excluded from parity totals and scripts: chatbots (different auth), legacy Clientside (deprecated)

> Note: the per-row `Endpoints` column in the Coverage by Section table sums higher than the Summary totals above. The discrepancy predates the BC5 baseline; the row count (48 sections) is authoritative for the `Since` column. Reconciling endpoint counts is pre-existing maintenance, tracked separately.

**SDK version:** the pin in `go.mod`, with
`internal/version/sdk-provenance.json` authoritative for the exact commit. That
pin is basecamp-sdk v0.23.0, which sends a check-in question's time as
`time_of_day` (basecamp/basecamp-sdk#968). v0.22.0 shipped
`Recordings().MoveToVault` (basecamp/basecamp-sdk#960). v0.21.0 shipped the Subtasks service
(basecamp/basecamp-sdk#883). v0.19.0 shipped the event-feed operations.
The command surface below largely dates to the v0.12.0 bump, which added 20 exported
Go methods over 13 new backend operations; the extra seven wrapped endpoints
that already existed but were reachable only through the raw generated client,
which the andon-cord rule forbids the CLI from calling. v0.13.0–v0.15.0
corrected shapes and routes (pointerized optional fields, page-selection
semantics, field-keyed 422 payloads) and added `files replace`, `files versions`,
and Bubble Up writes. v0.16.0 adds the three template-library operations.

Those methods land as four new command groups (`bookmarks`, `drafts`, `notes`,
`calendars`) and three extensions (`assignments` gains the Up Next verbs,
`todos create` gains `--loose`, `checkins` gains question pause/resume/notify/
answerers plus an account-wide `reminders` feed).

v0.12.0 also gave 11 `EverythingService` methods a trailing
`*EverythingTaskFilters` parameter — the nine paginated todo and card selectors
plus the two unpaginated overdue endpoints. The family is 5 unchanged + 11
changed = 16.

One v0.12.0 defect shaped a command rather than just a call: its
`parseErrorBody` read only `error`/`error_description`, so a calendar 422
carrying `{"errors":{"color":[…]}}` arrived as a bare `validation error` naming
neither field nor value. `calendars update` therefore validates its eleven
colors client-side. The SDK fix is inside the pin as of v0.13.0
(basecamp/basecamp-sdk#541 returns a field-keyed map); the client-side check
stays as a fast local answer, and the server's own message now backs it up.

It carries `EverythingService` (`AccountClient.Everything()`,
basecamp/basecamp-sdk#435 and #438), a 16-method account-wide aggregate family
covering cross-project messages, comments, checkins, forwards, files, and the
open/completed/unassigned/overdue/no-due-date todo and card rollups. **All 16
are reached from the CLI** — see [Account-wide
aggregates](#account-wide-aggregates).

The family was 17 methods through v0.10.0. `Everything().Boosts()` is gone as
of v0.11.0 (basecamp/basecamp-sdk#504): BC5 withdrew the `/boosts.json`
aggregate behind it (basecamp/bc3#12464), because its cost was proportional to
the account's accessible recordings rather than its boosts (~44s per page —
basecamp/bc3#12458). The feed is expected back later on a boost-proportional
query (basecamp/bc3#12463), but the endpoint is genuinely gone server-side in
the meantime, so the SDK dropped the operation rather than ship one that cannot
work. The CLI had already stopped calling it, so the removal landed here as a
no-op.

The aggregates are not a new command group and add
no endpoints to the tracked totals above: each aggregate is the account-wide
variant of a listing the CLI already owned, reached through that group's
existing leaf command. The contract is `ACCOUNT-WIDE-LISTINGS.md`.

Model and transport changes riding along:

- `UpdateCardRequest.Title`/`.Content`/`.DueOn` became `*string`, nil meaning
  "leave unchanged", for merge-safe partial updates (#489).
- `SearchResult.Content`/`.Description` became `*string`, and the excerpt moved
  to `PlainTextContent`/`PlainTextDescription` (#487). Those two are **HTML
  fragments despite the name** — BC3 wraps each query match in
  `<mark class="circled-text">` — so any consumer must strip markup before
  display.
- `BubbleUpURL` spread to `Recording`, `SearchResult`, `Todolist`, and
  `TodolistGroup` (#488); it previously existed only on `BubbleUp`. On `Todolist`
  and `TodolistGroup` the tag carries no `omitempty`, so the key is always
  present in machine output.
- HTTP 400 now maps to the `validation` error code rather than `api_error` (#482).
  `convertSDKError` passes the SDK code straight through, so a 400's JSON `code`
  changes `api_error` → `validation`. **Its exit code does not move: a 400 still
  exits 7.** `internal/output` defines no `validation` mapping, and `clioutput`
  defaults an unrecognised code to `ExitAPI` — so the new code lands on the same
  exit status the old one did. Exit 9 is not reachable from the CLI at all.
- Retry behavior: per-operation `retry.max` is honored as a ceiling (#483),
  `*WithBody` request bodies replay across retries (#481), and the declared
  `retry_on` status set is honored (#486).
- Provenance repinned to current bc3 HEAD, pinning the `participant_ids`
  contract (#491).

**Machine-output contract change.** `search` serializes raw SDK structs for
`--json`/`--agent`/`--md` (only the styled path is humanized), so these model
changes reach users directly: `content` and `description` now serialize as
explicit `null` (the pointer fields carry no `omitempty`, where the old empty
strings were omitted), and `plain_text_content`/`plain_text_description` plus
`bubble_up_url` appear when populated. Styled output is unaffected.

API date 2026-07-28.

## Account-wide aggregates

`EverythingService` answers, across every accessible project, the same questions
the project-scoped listings answer within one. All 16 methods are reachable.

These rows are **not** added to the totals above. They are not new endpoints in
the tracked matrix — they are the account-wide variant of listings already
counted, reached through the owning group's existing leaf command rather than a
new `everything` group. `--all-projects` pins the intent and overrides a
configured project; with nothing in scope the same command lists account-wide
instead of prompting for a project.

| Invocation | SDK method | Payload |
|------------|-----------|---------|
| `messages list --all-projects` | `Messages` | `[]Recording` |
| `comments list --all-projects` | `Comments` | `[]Recording` |
| `checkins answers --all-projects` | `Checkins` | `[]Recording` |
| `forwards list --all-projects` | `Forwards` | `[]Recording` |
| `files list --all-projects` | `Files` | `[]EverythingFile` |
| `todos list --all-projects` | `OpenTodos` | bucket groups |
| `todos list --all-projects --status completed` | `CompletedTodos` | bucket groups |
| `todos list --all-projects --unassigned` | `UnassignedTodos` | bucket groups |
| `todos list --all-projects --no-due-date` | `NoDueDateTodos` | bucket groups |
| `todos list --all-projects --overdue` | `OverdueTodos` | flat `[]Todo` |
| `cards list --all-projects` | `OpenCards` | bucket groups |
| `cards list --all-projects --status completed` | `CompletedCards` | bucket groups |
| `cards list --all-projects --unassigned` | `UnassignedCards` | bucket groups |
| `cards list --all-projects --no-due-date` | `NoDueDateCards` | bucket groups |
| `cards list --all-projects --not-now` | `NotNowCards` | bucket groups |
| `cards list --all-projects --overdue` | `OverdueCards` | flat `[]Card` |

`files list` additionally exposes the feed's own filters, `--kind`
(all/images/pdfs/documents/videos) and repeatable `--person`. Both are
account-wide-only: the project-scoped path has no equivalent filter, so passing
either with a project in scope is a usage error rather than a silent no-op.

`reports overdue` is neither replaced nor deprecated. It is a lateness-bucketed
report; `todos list --all-projects --overdue` is a flat oldest-first aggregate.

Design discussion: basecamp/basecamp-cli#585. Contract and invariants:
`ACCOUNT-WIDE-LISTINGS.md`.

## Coverage by Section

The **Since** column tags each row with the Basecamp version that introduced its section: `BC4` for sections that shipped before Basecamp 5, `BC5` for sections introduced in Basecamp 5. If a BC5 release adds endpoints to an existing BC4 section, split them into a new row tagged `BC5` rather than bumping the BC4 row's `Endpoints` count — that keeps the column unambiguous per row. Column dropped post-BC4 decommission.

**Status** is one of ✅ implemented, ⏭️ out of scope, or ⚠️ blocked — the CLI
cannot faithfully cover at least one endpoint for a reason outside the CLI. A
⚠️ row must name its blocker in Notes.

| Section | Endpoints | CLI Command | Status | Since | Priority | Notes |
|---------|-----------|-------------|--------|-------|----------|-------|
| **Core** |
| projects | 9 | `projects` | ✅ | BC4 | - | list, show, create, update, delete |
| projects (wallpaper) | 2 | `projects wallpaper` | ✅ | BC5 | - | set (`PUT /projects/:id/wallpaper.json`, multipart `wallpaper` with an optional `mode`), remove (`DELETE` on the same route; 204 with or without a wallpaper). `mode` sends `wallpaper_mode` through the existing `PUT /projects/:id.json`, with the current name the SDK requires. PNG, JPEG, GIF, WebP, AVIF, HEIF or HEIC up to 10 MB, checked before the upload using a local MIME hint; the server applies its shared image validations. People and personal assistants follow project-edit permissions; shared agents are refused with 403. Project JSON carries `wallpaper {url, mode}` when one is attached, so `projects show` shows it. Project-scoped via `--in`/`--project` |
| todos | 12 | `todos`, `todo`, `done`, `reopen` | ✅ | BC4 | - | list, show, create, update, complete, uncomplete, position, copy (BC5: `steps` shown on `todos show`, plus `subtasks_count`/`subtasks_completed_count`/`subtasks_url`; edit via `subtasks`). `todos create --loose` creates on the to-do set, outside any list |
| subtasks | 8 | `subtasks` | ✅ | BC5 | - | list (`GET /recordings/:id/subtasks.json`, paginated), show, create (`POST` on the same list route), update (partial; `--no-due`/`--no-assignees` clear), complete/uncomplete (`POST`/`DELETE /subtasks/:id/completion.json`), move (`PUT /subtasks/:id/position.json`, 1-based), delete. Account-scoped flat routes (bc3#12659), so no `--in`. Parents are to-dos and cards only. Same `Kanban::Step` records as `card_table_steps` |
| todolists | 9 | `todolists` | ✅ | BC4 | - | list, show, create, update, position, copy |
| todosets | 3 | `todosets` | ✅ | BC4 | - | Container for todolists, accessed via project dock (BC5: `todos_count`, `completed_loose_todos_count`, `todos_url`, `app_todos_url`) |
| todolist_groups | 8 | `todolistgroups` | ✅ | BC4 | - | list, show, create, update, position, copy |
| dock_tools | 7 | `tools` | ✅ | BC4 | - | Dock tool management: show, update, trash, enable, disable, reposition. `create` is BC5-only (create-by-type: `POST /buckets/{id}/dock/tools.json`), replacing the removed clone call; create-time `visible_to_clients` behind `tools create --visible-to-clients` (chat/kanban only) |
| **Hill Charts** |
| hill_charts | 2 | `hillcharts` | ✅ | BC4 | - | show, track/untrack todolists |
| gauges | 7 | `gauges` | ✅ | BC4 | - | list, needles, needle, create, update, delete, enable/disable |
| **Communication** |
| messages | 10 | `messages`, `message` | ✅ | BC4 | - | list, show, create, update, publish, pin, unpin. Create supports `--subscribe`/`--no-subscribe` and `--draft`. Publish promotes drafts to active. `move`/`copy` file a message onto a message board in another project (see recordings (moves, copies)) |
| message_boards | 3 | `messageboards` | ✅ | BC4 | - | Container, accessed via project dock |
| message_types | 5 | `messagetypes` | ✅ | BC4 | - | list, show, create, update, delete. Bucket-scoped (`/buckets/{id}/categories…`); commands are project-scoped via `--in`/`--project` |
| campfires | 14 | `chat` | ✅ | BC4 | - | list, messages, post, line show/update/delete. @mentions in content |
| pings | 3 | `pings` | ✅ | BC5 | - | list (`GET /circles.json`: active pings with at least one line, newest first, unpaginated), show (`GET /circles/:id.json`, archived included), start (`POST /circles.json`, which opens or reuses: an active direct ping is always reused, a group with the same people unless `--new-group`). A ping is a Circle bucket with one Campfire, so `messages`, `post` and `upload` run through the Campfire lines endpoints on the ping's `campfire_id`, with @mentions resolved among the ping's people. `<ping>` is an id, a ping URL, or a person (a direct ping: read from the list, or started when writing). `show` and `chat line` take ping URLs (`/circles/:id`, `/circles/:id@:line`). 403 `Pings have been disabled` and 422 (an agent starting a ping with nobody who can ping) surface as errors. Requires basecamp/bc3#13943 |
| comments | 8 | `comment`, `comments` | ✅ | BC4 | - | list, show, thread, create, update. @mentions in content. `show` surfaces `reply_target` + paste-ready `mention` from its single Get (no new calls). `thread` composes Get + parent recording (via type endpoint) + List into a deterministic reply-ready context (no new endpoints) |
| boosts | 7 | `boost`, `react` | ✅ | BC4 | - | list (recording + event), show, create (recording + event), update (`PUT /boosts/{id}`, the creator only; same 16-character cap as create), delete. No account-wide listing — BC5 withdrew `/boosts.json` (basecamp/bc3#12464); temporary, returns via basecamp/bc3#12463 |
| notifications | 2 | `notifications` | ✅ | BC4 | - | list, mark as read (BC5: `bubble_ups`/`scheduled_bubble_ups` sections; `memories` is BC4-only) |
| bubble_ups | 3 | `bubble-up`, `notifications bubbleups` | ✅ | BC5 | - | `bubble-up add`/`remove` create and delete a per-recording bubble-up (`POST`/`DELETE /recordings/{id}/bubble_up.json`); `add --at` schedules. Dedicated list is `notifications bubbleups` (`GET /my/readings/bubble_ups.json`, paginated) plus the `limit_bubble_ups` variant behind `notifications list --limit-bubble-ups`. Per-recording GET is an unrenderable API gap, so there is no `check`. |
| spotlights | 2 | `spotlight`, `unspotlight` | ✅ | BC5 | - | `POST`/`DELETE /recordings/{id}/spotlight.json` put a recording's card on, or take it off, its project or template home page; both idempotent. No endpoint reads spotlight state or lists spotlights, so there is no `check` or list. Reordering spotlights (`PUT /recordings/:id/spotlight/position`) has no SDK operation, so it is not covered |
| **Cards (Kanban)** |
| card_tables | 3 | `cards` | ✅ | BC4 | - | Accessed via project dock |
| card_table_cards | 9 | `cards` | ✅ | BC4 | - | list, show, create, update, move |
| card_table_columns | 11 | `cards columns` | ✅ | BC4 | - | list columns. SDK v0.12.0 added `Subscribe`/`Unsubscribe`; `cards column watch\|unwatch` already performs the same action through the generic recording-subscription endpoint and returns the resulting subscription details the specific endpoint does not, so the CLI keeps one spelling |
| card_table_steps | 4 | `cards steps` | ✅ | BC4 | - | Workflow steps on cards. The card-scoped aliases of `subtasks`, which bc3 keeps serving; `cards step move --position` is 1-based (the SDK refuses 0 since basecamp/basecamp-sdk#883) |
| card_table_wormholes | 3 | `cards wormholes` | ✅ | BC5 | - | list (via `wormholes[]` on card table), create, update, delete; `cards move --to-wormhole` teleports a card across projects and reports the new card once it lands |
| recordings (moves, copies) | 3 | `cards move --to-project`, `cards copy`, `todos move --to-project`/`--to-list`, `todos copy`, `todolists move --to-project`, `todolists copy`, `todolistgroups move --to-list`, `todolistgroups copy`, `messages move`, `messages copy`, `files move --to-project`, `files copy` | ✅ | BC5 | - | `POST /recordings/:id/moves.json` and `POST /recordings/:id/copies.json` file a card into a card table in this or another project, landing in Triage (Done if completed); `GET /filings/:id.json` reports the background filing, which the commands wait on before showing the new card. `--to-project` picks the project's only card table on its dock, `--to-card-table` one by ID, URL or name. `cards copy --include-comments` sends `include_comments`; a copy is never retried. To-dos file into a to-do list in any project (several IDs file one by one, and any failure fails the command naming what moved); a to-do list files into a project's To-dos (`--to-todoset` when it has several); a group files into a to-do list and stays a group. `--to-list`/`--to-todoset` take an ID, URL or a name looked up in `--to-project`; Basecamp refuses a group or an archived list as the destination. `messages move`/`copy` file a message onto a message board (`--to-project`, `--to-board`); a copy with no destination goes onto the message's own board. board. `files move --to-project`/`--to-folder` and `files copy` (also on `uploads`, `docs`, `vaults` and their aliases) file documents, uploads, cloud files, Google documents and folders into a project's Docs & Files, a folder inside it as `parent_id`; several IDs are filed one by one, as the web's bulk Move/Copy does |
| **Personal (My)** |
| my_bookmarks | 4 | `bookmarks` | ✅ | BC5 | - | list, check, add, remove. Private to the authenticated user; `add`/`remove` are idempotent, and `check` returns a bool reported in the payload rather than through the exit code. Bounded like the account-wide listings |
| drafts | 1 | `drafts` | ✅ | BC5 | - | list unpublished drafts across projects (server caps at 250). Bounded like the account-wide listings; publishing happens through the command for the draft's type |
| my_notes | 2 | `notes` | ✅ | BC5 | - | show, set. A singleton per person, so no id and no listing. Pre-first-write the record does not exist yet and renders as empty rather than 404. `set` writes Markdown as HTML; attachments are out of scope |
| **People** |
| people | 15 | `people`, `me` | ✅ | BC4 | - | list, show, update (edit your own profile via `PUT /my/profile.json`), out-of-office show/set/clear (`GetOutOfOffice`/`EnableOutOfOffice`/`DisableOutOfOffice`), pingable, add, remove (BC5: `tagline` alias of `bio` on person output). `people clients` covers the client side: `add`/`remove`/`invite` through `PUT /projects/:id/people/client_users.json` (basecamp/bc3#13098), `enable`/`disable` through `POST`/`DELETE /projects/:id/client_enablement.json`; `list` is the project roster filtered to `client: true` |
| **Search & Recordings** |
| my_assignments | 6 | `assignments` | ✅ | BC4 | - | list (priorities/non-priorities), completed, due (with scope filter), prioritize, deprioritize, reorder. `list` surfaces `priority_recording_id`, which is the only way to address a prioritized card-table step — it appears in no URL |
| search | 2 | `search` | ✅ | BC4 | - | Full-text search + metadata. Filters: `--project`/`--in`, `--type`, `--creator`, `--since` (BC5-only), `--file-type`, `--exclude-chat`. Metadata lists recording/file search types |
| recordings | 4 | `recordings` | ✅ | BC4 | - | Browse by type/status, trash/archive/restore |
| **Files & Documents** |
| uploads | 8 | `files`, `uploads` | ✅ | BC4 | - | list, show, create, update, download, versions (`files versions <id>`), replace (`files replace <id> <file>`); trash/archive/restore go through `recordings`. Create supports `--visible-to-clients` (root vault only) |
| vaults | 8 | `files`, `vaults` | ✅ | BC4 | - | list, show, create |
| vaults (move) | 1 | `files move` | ✅ | BC5 | - | `POST /recordings/:id/filing.json`: moves a document, upload or folder into another folder in the same project, in place (same id and comments). `--to` takes a folder ID or URL; `--position` is 1-indexed. Flat route, so no `--in`. Moves and copies into another project go through recordings (moves, copies) with `--to-project`/`--to-folder` |
| documents | 8 | `files`, `docs` | ✅ | BC4 | - | list, show, create, update. Create supports `--subscribe`/`--no-subscribe`, `--visible-to-clients` (root vault only) |
| attachments | 1 | `uploads`, `attachments` | ✅ | BC4 | - | Upload via `attach`; list embedded attachments via `attachments list` (parses `<bc-attachment>` from content) |
| embeds | 2 | `embeds` | ✅ | BC5 | - | create, show. Rich-text commands also embed a URL that stands alone on its line in Markdown content, through the same create |
| **Schedule** |
| calendars | 2 | `calendars` | ✅ | BC5 | - | show, update (color only). No index endpoint, so there is no `calendars list` — address one by id or pasted URL. The eleven colors are validated client-side for an immediate answer that names the alternatives; since v0.13.0 the SDK also carries the server's field-keyed 422 message |
| schedules | 2 | `schedule` | ✅ | BC4 | - | Schedule container + settings |
| schedule_entries | 5 | `schedule` | ✅ | BC4 | - | list, show, create, update, occurrences. Create supports `--subscribe`/`--no-subscribe` |
| events | 1 | `events` | ✅ | BC4 | - | Recording change audit trail |
| **Event Feed** |
| event_feed | 3 | `events poll`, `events ticket`, `inbox` | ✅ | BC5 | - | The account-wide event feed: `GET /events.json` (`events poll`), `GET /inbox.json` (`inbox`, agent principals only), `POST /events/stream_ticket.json` (`events ticket`). Pagination is the body envelope — a durable `position` and a `next` continuation URL — not the Link-header page walk, so `--all` walks `next` rather than page numbers. The live WebSocket lane the ticket opens is out of scope for the CLI: the SDK's cable client carries a WebSocket dependency the CLI does not take. The mint is a CLI command only — it is excluded from the MCP surface, whose dispatcher would return the bearer verbatim into a model transcript and which could not open the socket anyway |
| **Webhooks** |
| webhooks | 7 | `webhooks` | ✅ | BC4 | - | list, show, create, update, delete |
| **Templates** |
| templates | 10 | `templates` | ✅ | BC4 | - | list, show, create, update, delete, construct, construction, library, copy, copy-status |
| **Time Tracking** |
| timesheets | 6 | `timesheet` | ✅ | BC4 | - | list, show, create, update, delete |
| **Subscriptions** |
| subscriptions | 4 | `subscriptions` | ✅ | BC4 | - | show, subscribe, unsubscribe, add/remove |
| **Check-ins (Automatic)** |
| questionnaires | 2 | `checkins` | ✅ | BC4 | - | Container for check-in questions |
| questions | 8 | `checkins` | ✅ | BC4 | - | list, show, create, update, pause, resume, notification settings, answerers (`checkins question notify` is tri-state per setting; `answerers` takes no `--page`, since the SDK does not honor one) |
| question_answers | 4 | `checkins` | ✅ | BC4 | - | list, show |
| question_reminders | 1 | `checkins reminders` | ✅ | BC5 | - | Account-wide pending-reminder feed (`GET /my/question_reminders.json`). `--limit` is a real SDK-side bound; no `--page`, since the options struct does not honor a page number |
| **Inbox (Email Forwards)** |
| inboxes | 1 | `forwards` | ✅ | BC4 | - | Inbox container |
| forwards | 2 | `forwards` | ✅ | BC4 | - | list, show |
| inbox_replies | 2 | `forwards` | ✅ | BC4 | - | list replies, show reply |
| **Clients** |
| client_visibility | 1 | `recordings visibility` | ✅ | BC4 | - | Toggle client visibility on recordings |
| **Client Portal (Legacy Clientside)** |
| client_approvals | 6 | - | ⏭️ | BC4 | skip | Legacy Clientside only (see notes) |
| client_correspondences | 6 | - | ⏭️ | BC4 | skip | Legacy Clientside only (see notes) |
| client_replies | 6 | - | ⏭️ | BC4 | skip | Legacy Clientside only (see notes) |
| **Chatbots** |
| chatbots | 10 | - | ⏭️ | BC4 | skip | Requires chatbot key, not OAuth (see notes) |
| **Account** |
| account | 4 | `accounts` | ✅ | BC4 | - | show, update name, upload logo, remove logo |
| **Lineup** |
| lineup_markers | 4 | `lineup` | ✅ | BC4 | - | list, create, update, delete markers |
| **Reference Only** |
| basecamps | 0 | - | - | - | - | Documentation reference, no endpoints |
| rich_text | 0 | - | - | - | - | Documentation reference, no endpoints |

## Priority Guide

- **high**: Core workflow, frequently needed
- **medium**: Useful but not critical path
- **low**: Specialized, rarely needed
- **skip**: Out of scope (client portal, chatbots, internal)

## Remaining (Intentionally Skipped)

All remaining sections are intentionally out of scope:
- **chatbots** (10 endpoints) - Requires chatbot key auth, not OAuth
- **client_approvals/correspondences/replies** (18 endpoints) - Legacy Clientside portal
These are excluded from doc parity totals.

## Skipped Sections

### Client Portal (`client_approvals`, `client_correspondences`, `client_replies`) - Legacy "Clientside"

These endpoints are for the **legacy "Clientside"** feature (the dedicated client portal area), which is distinct from the modern "clients as project participants" model.

**Why skipped:**
- Confusingly similar naming to modern client setup
- Legacy feature with limited adoption
- Requires projects with specific client portal configuration
- Unlikely to be needed in typical developer/agent workflows

**Note:** The `client_visibility` endpoint IS implemented (via `basecamp recordings visibility`) because it's part of the **modern** clients setup for controlling what client participants can see on any recording.

### Chatbots

The chatbots API uses a **chatbot key** for authentication rather than OAuth tokens. This is a fundamentally different auth model:
- Chatbot keys are per-integration, not per-user
- They're designed for automated integrations (Slack bots, etc.)
- The CLI uses OAuth for user-scoped access

Supporting chatbot auth would require a separate configuration path. If chatbot functionality is needed, a dedicated chatbot-specific tool would be more appropriate.

## Implementation Notes

### Endpoint Patterns

Each resource typically supports:
- `GET /...` - List
- `GET /.../:id` - Show
- `POST /...` - Create
- `PUT /.../:id` - Update
- `DELETE /.../:id` - Trash (soft delete)

Plus action endpoints:
- `POST /.../:id/completion` - Complete (todos)
- `DELETE /.../:id/completion` - Uncomplete (todos)
- `PUT /.../:id/position` - Reorder
- `POST /.../:id/pin` - Pin to top
- `DELETE /.../:id/pin` - Unpin
- `PUT /.../:id/status/:status` - Change status (trash/archive/restore)

### CLI Command Patterns

```bash
basecamp <resource>                    # List (default)
basecamp <resource> list               # List (explicit)
basecamp <resource> show <id>          # Show details
basecamp <resource> <id>               # Show (shorthand)
basecamp <resource> create "..."       # Create new
basecamp <resource> update <id>        # Update existing
basecamp <singular> "..."              # Create (shorthand)
```

## Verification

API coverage is manually tracked in this document. The coverage matrix above is updated when new endpoints are implemented.

To verify a specific endpoint is implemented, check the corresponding command in `internal/commands/`.
