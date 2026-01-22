# bcq API Coverage Matrix

Coverage of Basecamp 3 API endpoints. Source: [bc3-api/sections](https://github.com/basecamp/bc3-api).

## Summary

| Status | Sections | Endpoints |
|--------|----------|-----------|
| ✅ Implemented | 36 | 130 |
| ⏭️ Out of scope | 4 | 12 |
| **Total (docs)** | **40** | **142** |

**100% coverage of in-scope API** (130/130 endpoints)

Out-of-scope sections are excluded from parity totals and scripts: chatbots (different auth), legacy Clientside (deprecated)

## Coverage by Section

| Section | Endpoints | bcq Command | Status | Priority | Notes |
|---------|-----------|-------------|--------|----------|-------|
| **Core** |
| projects | 9 | `projects` | ✅ | - | list, show, create, update, delete |
| todos | 11 | `todos`, `todo`, `done`, `reopen` | ✅ | - | list, show, create, complete, uncomplete, position |
| todolists | 8 | `todolists` | ✅ | - | list, show, create, update |
| todosets | 3 | `todosets` | ✅ | - | Container for todolists, accessed via project dock |
| todolist_groups | 8 | `todolistgroups` | ✅ | - | list, show, create, update, position |
| **Communication** |
| messages | 10 | `messages`, `message` | ✅ | - | list, show, create, update, pin, unpin |
| message_boards | 3 | `messageboards` | ✅ | - | Container, accessed via project dock |
| message_types | 5 | `messagetypes` | ✅ | - | list, show, create, update, delete |
| campfires | 14 | `campfire` | ✅ | - | list, messages, post, line show/delete |
| comments | 8 | `comment`, `comments` | ✅ | - | list, show, create, update |
| **Cards (Kanban)** |
| card_tables | 3 | `cards` | ✅ | - | Accessed via project dock |
| card_table_cards | 9 | `cards` | ✅ | - | list, show, create, update, move |
| card_table_columns | 11 | `cards columns` | ✅ | - | list columns |
| card_table_steps | 4 | `cards steps` | ✅ | - | Workflow steps on cards |
| **People** |
| people | 12 | `people`, `me` | ✅ | - | list, show, pingable, add, remove |
| **Search & Recordings** |
| search | 2 | `search` | ✅ | - | Full-text search |
| recordings | 4 | `recordings` | ✅ | - | Browse by type/status, trash/archive/restore |
| **Files & Documents** |
| uploads | 8 | `files`, `uploads` | ✅ | - | list, show |
| vaults | 8 | `files`, `vaults` | ✅ | - | list, show, create |
| documents | 8 | `files`, `docs` | ✅ | - | list, show, create, update |
| attachments | 1 | `uploads` | ✅ | - | Attachment metadata |
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
| **Clients** |
| client_visibility | 1 | `recordings visibility` | ✅ | - | Toggle client visibility on recordings |
| **Client Portal (Legacy Clientside)** |
| client_approvals | 6 | - | ⏭️ | skip | Legacy Clientside only (see notes) |
| client_correspondences | 6 | - | ⏭️ | skip | Legacy Clientside only (see notes) |
| client_replies | 6 | - | ⏭️ | skip | Legacy Clientside only (see notes) |
| **Chatbots** |
| chatbots | 10 | - | ⏭️ | skip | Requires chatbot key, not OAuth (see notes) |
| **Lineup** |
| lineup_markers | 3 | `lineup` | ✅ | - | create, update, delete markers |
| **Reference Only** |
| basecamps | 0 | - | - | - | Documentation reference, no endpoints |
| rich_text | 0 | - | - | - | Documentation reference, no endpoints |

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

**Note:** The `client_visibility` endpoint IS implemented (via `bcq recordings visibility`) because it's part of the **modern** clients setup for controlling what client participants can see on any recording.

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

## Verification

API coverage is verified automatically via test infrastructure in `test/api_coverage/`.

### Running Coverage Tests

```bash
# Run all API coverage tests
bats test/api_coverage.bats

# Generate coverage report
./test/api_coverage/compare.sh

# JSON output for programmatic use
./test/api_coverage/compare.sh --json

# Verbose mode (shows extra endpoints in implementation)
./test/api_coverage/compare.sh --verbose
```

### Test Infrastructure

| File | Purpose |
|------|---------|
| `test/api_coverage/extract_docs.sh` | Fetches endpoints from GitHub bc3-api repo (cached 1hr) |
| `test/api_coverage/extract_impl.sh` | Parses bcq source for API calls |
| `test/api_coverage/exclusions.txt` | Lists out-of-scope sections |
| `test/api_coverage/compare.sh` | Compares documented vs implemented endpoints |
| `test/api_coverage.bats` | Automated coverage verification tests |

### Data Source

Endpoints are fetched from the canonical [bc3-api](https://github.com/basecamp/bc3-api) GitHub repository by default. Results are cached for 1 hour in `~/.cache/bcq/api_docs/`.

```bash
# Use local clone instead of GitHub (faster, offline)
BC3_API_DIR=~/Work/basecamp/bc3-api ./test/api_coverage/extract_docs.sh --local

# Clear cache to force re-fetch
rm -rf ~/.cache/bcq/api_docs
```

### CI Integration

The coverage tests are designed for CI integration:
- Fetch from GitHub API (no local clone required)
- Exit non-zero if coverage drops below threshold
- Produce JSON output for reporting

```bash
# Fail if any documented endpoints are missing
./test/api_coverage/compare.sh --check-missing

# Verify counts match COVERAGE.md claims
./test/api_coverage/compare.sh --verify-counts
```
