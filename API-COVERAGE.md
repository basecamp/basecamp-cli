# Basecamp CLI API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 42 | 155 |
| ⏭️ Out of scope | 4 | 12 |
| **Total (docs)** | **46** | **167** |

**100% coverage of in-scope API** (155/155 endpoints)

Out-of-scope sections are excluded from parity totals and scripts: chatbots (different auth), legacy Clientside (deprecated)

**SDK version:** v0.7.3 — maintenance bump: API date advanced to 2026-03-23, transitive dependency updates. No new services or methods.

## Coverage by Section

The **Since** column tracks when each endpoint became available: `BC4` for endpoints that shipped before Basecamp 5, `BC5` for additive BC5-only endpoints. The column is dropped post-BC4 decommission.

| Section | Endpoints | CLI Command | Status | Since | Priority | Notes |
|---------|-----------|-------------|--------|-------|----------|-------|
| **Core** |
| projects | 9 | `projects` | ✅ | BC4 | - | list, show, create, update, delete |
| todos | 11 | `todos`, `todo`, `done`, `reopen` | ✅ | BC4 | - | list, show, create, update, complete, uncomplete, position |
| todolists | 8 | `todolists` | ✅ | BC4 | - | list, show, create, update |
| todosets | 3 | `todosets` | ✅ | BC4 | - | Container for todolists, accessed via project dock |
| todolist_groups | 8 | `todolistgroups` | ✅ | BC4 | - | list, show, create, update, position |
| **Hill Charts** |
| hill_charts | 2 | `hillcharts` | ✅ | BC4 | - | show, track/untrack todolists |
| gauges | 7 | `gauges` | ✅ | BC4 | - | list, needles, needle, create, update, delete, enable/disable |
| **Communication** |
| messages | 10 | `messages`, `message` | ✅ | BC4 | - | list, show, create, update, publish, pin, unpin. Create supports `--subscribe`/`--no-subscribe` and `--draft`. Publish promotes drafts to active |
| message_boards | 3 | `messageboards` | ✅ | BC4 | - | Container, accessed via project dock |
| message_types | 5 | `messagetypes` | ✅ | BC4 | - | list, show, create, update, delete |
| campfires | 14 | `chat` | ✅ | BC4 | - | list, messages, post, line show/delete. @mentions in content |
| comments | 8 | `comment`, `comments` | ✅ | BC4 | - | list, show, create, update. @mentions in content |
| boosts | 6 | `boost`, `react` | ✅ | BC4 | - | list (recording + event), show, create (recording + event), delete |
| notifications | 2 | `notifications` | ✅ | BC4 | - | list, mark as read |
| **Cards (Kanban)** |
| card_tables | 3 | `cards` | ✅ | BC4 | - | Accessed via project dock |
| card_table_cards | 9 | `cards` | ✅ | BC4 | - | list, show, create, update, move |
| card_table_columns | 11 | `cards columns` | ✅ | BC4 | - | list columns |
| card_table_steps | 4 | `cards steps` | ✅ | BC4 | - | Workflow steps on cards |
| **People** |
| people | 12 | `people`, `me` | ✅ | BC4 | - | list, show, pingable, add, remove |
| **Search & Recordings** |
| my_assignments | 3 | `assignments` | ✅ | BC4 | - | list (priorities/non-priorities), completed, due (with scope filter) |
| search | 2 | `search` | ✅ | BC4 | - | Full-text search |
| recordings | 4 | `recordings` | ✅ | BC4 | - | Browse by type/status, trash/archive/restore |
| **Files & Documents** |
| uploads | 8 | `files`, `uploads` | ✅ | BC4 | - | list, show |
| vaults | 8 | `files`, `vaults` | ✅ | BC4 | - | list, show, create |
| documents | 8 | `files`, `docs` | ✅ | BC4 | - | list, show, create, update. Create supports `--subscribe`/`--no-subscribe` |
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
