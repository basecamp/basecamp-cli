# Basecamp CLI API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 45 | 167 |
| ⏭️ Out of scope | 4 | 12 |
| **Total tracked** | **49** | **179** |

**100% coverage of tracked in-scope API** (167/167 endpoints). This is not a
complete bc-api parity figure. The other five BC5 sections introduced by
bc-api#410 remain untracked and outside this coverage matrix. The pinned SDK's
`EverythingService` is now fully reached — see [Account-wide
aggregates](#account-wide-aggregates).

Out-of-scope sections are excluded from parity totals and scripts: chatbots (different auth), legacy Clientside (deprecated)

> Note: the per-row `Endpoints` column in the Coverage by Section table sums higher than the Summary totals above. The discrepancy predates the BC5 baseline; the row count (48 sections) is authoritative for the `Since` column. Reconciling endpoint counts is pre-existing maintenance, tracked separately.

**SDK version:** v0.11.0 — carries `EverythingService`
(`AccountClient.Everything()`, basecamp/basecamp-sdk#435 and #438), a
16-method account-wide aggregate family covering cross-project messages,
comments, checkins, forwards, files, and the
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
  Since `convertSDKError` passes the SDK code straight through, **a 400's exit
  code moves from 7 to 9**; 422 was already validation.
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
the project-scoped listings answer within one. All 17 methods are reachable.

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

| Section | Endpoints | CLI Command | Status | Since | Priority | Notes |
|---------|-----------|-------------|--------|-------|----------|-------|
| **Core** |
| projects | 9 | `projects` | ✅ | BC4 | - | list, show, create, update, delete |
| todos | 11 | `todos`, `todo`, `done`, `reopen` | ✅ | BC4 | - | list, show, create, update, complete, uncomplete, position (BC5: `steps` shown on `todos show`; edit via `cards step`) |
| todolists | 9 | `todolists` | ✅ | BC4 | - | list, show, create, update, position |
| todosets | 3 | `todosets` | ✅ | BC4 | - | Container for todolists, accessed via project dock (BC5: `todos_count`, `completed_loose_todos_count`, `todos_url`, `app_todos_url`) |
| todolist_groups | 8 | `todolistgroups` | ✅ | BC4 | - | list, show, create, update, position |
| dock_tools | 7 | `tools` | ✅ | BC4 | - | Dock tool management: show, update, trash, enable, disable, reposition. `create` is BC5-only (create-by-type: `POST /buckets/{id}/dock/tools.json`), replacing the removed clone call; create-time `visible_to_clients` behind `tools create --visible-to-clients` (chat/kanban only) |
| **Hill Charts** |
| hill_charts | 2 | `hillcharts` | ✅ | BC4 | - | show, track/untrack todolists |
| gauges | 7 | `gauges` | ✅ | BC4 | - | list, needles, needle, create, update, delete, enable/disable |
| **Communication** |
| messages | 10 | `messages`, `message` | ✅ | BC4 | - | list, show, create, update, publish, pin, unpin. Create supports `--subscribe`/`--no-subscribe` and `--draft`. Publish promotes drafts to active |
| message_boards | 3 | `messageboards` | ✅ | BC4 | - | Container, accessed via project dock |
| message_types | 5 | `messagetypes` | ✅ | BC4 | - | list, show, create, update, delete. Bucket-scoped (`/buckets/{id}/categories…`); commands are project-scoped via `--in`/`--project` |
| campfires | 14 | `chat` | ✅ | BC4 | - | list, messages, post, line show/update/delete. @mentions in content |
| comments | 8 | `comment`, `comments` | ✅ | BC4 | - | list, show, thread, create, update. @mentions in content. `show` surfaces `reply_target` + paste-ready `mention` from its single Get (no new calls). `thread` composes Get + parent recording (via type endpoint) + List into a deterministic reply-ready context (no new endpoints) |
| boosts | 6 | `boost`, `react` | ✅ | BC4 | - | list (recording + event), show, create (recording + event), delete. No account-wide listing — BC5 withdrew `/boosts.json` (basecamp/bc3#12464); temporary, returns via basecamp/bc3#12463 |
| notifications | 2 | `notifications` | ✅ | BC4 | - | list, mark as read (BC5: `bubble_ups`/`scheduled_bubble_ups` sections; `memories` is BC4-only) |
| bubble_ups | 1 | `notifications bubbleups` | ✅ | BC5 | - | Dedicated Bubble Ups list (`GET /my/readings/bubble_ups.json`, paginated) plus the `limit_bubble_ups` variant behind `notifications list --limit-bubble-ups` |
| **Cards (Kanban)** |
| card_tables | 3 | `cards` | ✅ | BC4 | - | Accessed via project dock |
| card_table_cards | 9 | `cards` | ✅ | BC4 | - | list, show, create, update, move |
| card_table_columns | 11 | `cards columns` | ✅ | BC4 | - | list columns |
| card_table_steps | 4 | `cards steps` | ✅ | BC4 | - | Workflow steps on cards |
| card_table_wormholes | 3 | `cards wormholes` | ✅ | BC5 | - | list (via `wormholes[]` on card table), create, update, delete; `cards move --to-wormhole` teleports a card across projects (async, new id) |
| **People** |
| people | 12 | `people`, `me` | ✅ | BC4 | - | list, show, pingable, add, remove (BC5: `tagline` alias of `bio` on person output) |
| **Search & Recordings** |
| my_assignments | 3 | `assignments` | ✅ | BC4 | - | list (priorities/non-priorities), completed, due (with scope filter) |
| search | 2 | `search` | ✅ | BC4 | - | Full-text search + metadata. Filters: `--project`/`--in`, `--type`, `--creator`, `--since` (BC5-only), `--file-type`, `--exclude-chat`. Metadata lists recording/file search types |
| recordings | 4 | `recordings` | ✅ | BC4 | - | Browse by type/status, trash/archive/restore |
| **Files & Documents** |
| uploads | 8 | `files`, `uploads` | ✅ | BC4 | - | list, show, create. Create supports `--visible-to-clients` (root vault only) |
| vaults | 8 | `files`, `vaults` | ✅ | BC4 | - | list, show, create |
| documents | 8 | `files`, `docs` | ✅ | BC4 | - | list, show, create, update. Create supports `--subscribe`/`--no-subscribe`, `--visible-to-clients` (root vault only) |
| attachments | 1 | `uploads`, `attachments` | ✅ | BC4 | - | Upload via `attach`; list embedded attachments via `attachments list` (parses `<bc-attachment>` from content) |
| **Schedule** |
| schedules | 2 | `schedule` | ✅ | BC4 | - | Schedule container + settings |
| schedule_entries | 5 | `schedule` | ✅ | BC4 | - | list, show, create, update, occurrences. Create supports `--subscribe`/`--no-subscribe` |
| events | 1 | `events` | ✅ | BC4 | - | Recording change audit trail |
| **Webhooks** |
| webhooks | 7 | `webhooks` | ✅ | BC4 | - | list, show, create, update, delete |
| **Templates** |
| templates | 7 | `templates` | ✅ | BC4 | - | list, show, create, update, delete, construct, construction |
| **Time Tracking** |
| timesheets | 6 | `timesheet` | ✅ | BC4 | - | list, show, create, update, delete |
| **Subscriptions** |
| subscriptions | 4 | `subscriptions` | ✅ | BC4 | - | show, subscribe, unsubscribe, add/remove |
| **Check-ins (Automatic)** |
| questionnaires | 2 | `checkins` | ✅ | BC4 | - | Container for check-in questions |
| questions | 5 | `checkins` | ✅ | BC4 | - | list, show, create, update |
| question_answers | 4 | `checkins` | ✅ | BC4 | - | list, show |
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
