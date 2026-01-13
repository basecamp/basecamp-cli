# bcq API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 27 | ~130 |
| 🔶 Partial | 2 | ~6 |
| ⬜ Not started | 5 | ~15 |
| ⏭️ Skip | 8 | ~20 |
| **Total** | **42** | **~170** |

## Coverage by Section

| Section | Endpoints | bcq Command | Status | Priority | Notes |
|---------|-----------|-------------|--------|----------|-------|
| **Core** |
| projects | 9 | `projects` | ✅ | - | list, show, create, update, delete |
| todos | 11 | `todos`, `todo`, `done`, `reopen` | ✅ | - | list, show, create, complete, uncomplete, position |
| todolists | 8 | `todolists` | ✅ | - | list, show, create, update |
| todosets | 3 | - | 🔶 | low | Container for todolists, accessed via project dock |
| todolist_groups | 8 | `todolistgroups` | ✅ | - | list, show, create, update, position |
| **Communication** |
| messages | 10 | `messages`, `message` | ✅ | - | list, show, create, update, pin, unpin |
| message_boards | 3 | - | 🔶 | low | Container, accessed via project dock |
| message_types | 5 | `messagetypes` | ✅ | - | list, show, create, update, delete |
| campfires | 14 | `campfire` | ✅ | - | list, messages, post, line show/delete |
| comments | 8 | `comment`, `comments` | ✅ | - | list, show, create, update |
| **Cards (Kanban)** |
| card_tables | 3 | `cards` | ✅ | - | Accessed via project dock |
| card_table_cards | 9 | `cards` | ✅ | - | list, show, create, update, move |
| card_table_columns | 11 | `cards columns` | ✅ | - | list columns |
| card_table_steps | 4 | - | ⬜ | medium | Workflow steps on cards |
| **People** |
| people | 12 | `people`, `me` | ✅ | - | list, show, pingable, add, remove |
| **Search & Recordings** |
| search | 2 | `search` | ✅ | - | Full-text search |
| recordings | 4 | `recordings` | ✅ | - | Browse by type/status, trash/archive/restore |
| **Files & Documents** |
| uploads | 8 | `files`, `uploads` | ✅ | - | list, show |
| vaults | 8 | `files`, `vaults` | ✅ | - | list, show, create |
| documents | 8 | `files`, `docs` | ✅ | - | list, show, create, update |
| attachments | 1 | - | ⬜ | medium | Attachment metadata |
| **Schedule** |
| schedules | 2 | `schedule` | ✅ | - | Schedule container + settings |
| schedule_entries | 5 | `schedule` | ✅ | - | list, show, create, update, occurrences |
| events | 1 | `events` | ✅ | - | Recording change audit trail |
| **Webhooks** |
| webhooks | 7 | `webhooks` | ✅ | - | list, show, create, update, delete |
| **Templates** |
| templates | 7 | `templates` | ✅ | - | list, show, create, update, delete, construct, construction |
| **Time Tracking** |
| timesheets | 6 | `timesheet` | ✅ | - | list, show, create, update, delete |
| **Subscriptions** |
| subscriptions | 4 | `subscriptions` | ✅ | - | show, subscribe, unsubscribe, add/remove |
| **Check-ins (Automatic)** |
| questionnaires | 2 | `checkins` | ✅ | - | Container for check-in questions |
| questions | 5 | `checkins` | ✅ | - | list, show, create, update |
| question_answers | 4 | `checkins` | ✅ | - | list, show |
| **Inbox (Email Forwards)** |
| inboxes | 1 | `forwards` | ✅ | - | Inbox container |
| forwards | 2 | `forwards` | ✅ | - | list, show |
| inbox_replies | 2 | `forwards` | ✅ | - | list replies, show reply |
| **Client Portal** |
| client_approvals | 6 | - | ⏭️ | skip | Client portal only (see notes) |
| client_correspondences | 6 | - | ⏭️ | skip | Client portal only (see notes) |
| client_replies | 6 | - | ⏭️ | skip | Client portal only (see notes) |
| client_visibility | 1 | - | ⏭️ | skip | Client portal only (see notes) |
| **Chatbots** |
| chatbots | 10 | - | ⏭️ | skip | Requires chatbot key, not OAuth (see notes) |
| **Other** |
| lineup_markers | 3 | - | ⏭️ | skip | Lineup feature markers |
| basecamps | 0 | - | ⏭️ | skip | Reference only |
| rich_text | 0 | - | ⏭️ | skip | Reference only |

## Priority Guide

- **high**: Core workflow, frequently needed
- **medium**: Useful but not critical path
- **low**: Specialized, rarely needed
- **skip**: Out of scope (client portal, chatbots, internal)

## Next Up (Medium Priority)

1. **card_table_steps** (4 endpoints) - Workflow steps on cards
2. **attachments** (1 endpoint) - Attachment metadata

## Skipped Sections

### Client Portal (`client_*`)

The client portal endpoints (`client_approvals`, `client_correspondences`, `client_replies`, `client_visibility`) are specific to client-facing features. They require:
- Projects with client access enabled
- Client users (external to the organization)
- Specific client workflow context

These are unlikely to be needed in typical developer/agent workflows and add complexity without broad utility.

### Chatbots

The chatbots API uses a **chatbot key** for authentication rather than OAuth tokens. This is a fundamentally different auth model:
- Chatbot keys are per-integration, not per-user
- They're designed for automated integrations (Slack bots, etc.)
- bcq uses OAuth for user-scoped access

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

### bcq Command Patterns

```bash
bcq <resource>                    # List (default)
bcq <resource> list               # List (explicit)
bcq <resource> show <id>          # Show details
bcq <resource> <id>               # Show (shorthand)
bcq <resource> create "..."       # Create new
bcq <resource> update <id>        # Update existing
bcq <singular> "..."              # Create (shorthand)
```
