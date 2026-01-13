# bcq API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 18 | ~110 |
| 🔶 Partial | 2 | ~8 |
| ⬜ Not started | 14 | ~45 |
| ⏭️ Skip | 8 | ~20 |
| **Total** | **42** | **~180** |

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
| message_types | 9 | - | ⬜ | low | Announcement categories |
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
| schedules | 4 | - | ⬜ | medium | Schedule container |
| schedule_entries | 9 | - | ⬜ | medium | Calendar events |
| events | 3 | - | ⬜ | low | Event occurrences |
| **Webhooks** |
| webhooks | 7 | `webhooks` | ✅ | - | list, show, create, update, delete |
| **Templates** |
| templates | 15 | - | ⬜ | low | Project templates |
| **Time Tracking** |
| timesheets | 9 | - | ⬜ | medium | Time entries |
| **Subscriptions** |
| subscriptions | 8 | - | ⬜ | low | Notification subscriptions |
| **Check-ins (Automatic)** |
| questionnaires | 3 | - | ⬜ | low | Check-in questions container |
| questions | 6 | - | ⬜ | low | Check-in questions |
| question_answers | 6 | - | ⬜ | low | Check-in answers |
| **Inbox (Email Forwards)** |
| inboxes | 3 | - | ⬜ | low | Email forward inbox |
| inbox_replies | 6 | - | ⬜ | low | Replies to forwards |
| forwards | 6 | - | ⬜ | low | Forwarded emails |
| **Client Portal** |
| client_approvals | 6 | - | ⏭️ | skip | Client-specific |
| client_correspondences | 6 | - | ⏭️ | skip | Client-specific |
| client_replies | 6 | - | ⏭️ | skip | Client-specific |
| client_visibility | 1 | - | ⏭️ | skip | Client-specific |
| **Chatbots** |
| chatbots | 10 | - | ⏭️ | skip | Integration-specific |
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

1. **schedules** (4 endpoints) - Schedule container
2. **schedule_entries** (9 endpoints) - Calendar events
3. **timesheets** (9 endpoints) - Time entries
4. **card_table_steps** (4 endpoints) - Workflow steps on cards

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
