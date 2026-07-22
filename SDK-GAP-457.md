# SDK Gap: create-time `visible_to_clients` (CLI issue #457)

**Status:** blocks basecamp-cli #457 — "Expose client visibility on
recording-creating commands."

**Lane note:** filed from the CLI repo. Repo policy (`AGENTS.md`) forbids
bypassing the SDK wrappers with raw generated-client calls and directs SDK
blockers to an SDK issue; it does not categorically forbid SDK-repo changes. This
document is the design communiqué behind that SDK issue.

**SDK tracking issue (the unblocker):**
[basecamp/basecamp-sdk#395](https://github.com/basecamp/basecamp-sdk/issues/395).
That issue, not this file, is the durable record.

**Lifecycle:** this file is a transient handoff artifact. It is deleted in the
same commit that wires the feature once the SDK unblocks; the SDK issue and this
PR remain the durable record.

## What the CLI needs

A way to set a recording's client visibility **at create time**, in the same
POST that creates the record, through the Go SDK's create-request types — so
`basecamp messages create --visible-to-clients` is one atomic call rather than a
create-then-toggle follow-up.

## Server already supports it

Basecamp (bc3 **master**) accepts a top-level boolean `visible_to_clients` on the
create request. The field is a sibling of the recordable fields in the POST body.
For messages the request is:

```
POST /message_boards/:board_id/messages.json
```

```json
{ "subject": "…", "content": "…", "visible_to_clients": true }
```

(The message fields are flat in the SDK/wire body — Rails ParamsWrapper wraps
them into `params[:message]` server-side while `visible_to_clients` stays a
top-level key, which is what the controller reads. A legacy route
`/buckets/:bucket/message_boards/:board_id/messages.json` also exists.)

Implemented by the `Recording::VisibleToClientsParam` controller concern
(`app/controllers/concerns/recording/visible_to_clients_param.rb`). Semantics:
- **Omitted → inherit** the parent recording's visibility (falls back to
  `false`). This is why the SDK field must be tri-state (see below): absent must
  mean "inherit", explicit `false` must mean "team-only".
- Client users are always forced `true`.
- The documented public API section (`doc/api/sections/client_visibility.md`)
  only covers the separate toggle endpoint; the create-time param is implemented
  but undocumented there.

### Recording types that accept it at create

Docked/top-level recordings whose create controller includes the concern **and**
that have a CLI create command:

| Recording         | Controller                        | SDK create input           | Initial scope |
|-------------------|-----------------------------------|----------------------------|---------------|
| Message           | `messages_controller.rb`          | `CreateMessageInput`         | yes (priority) |
| Todolist          | `todolists_controller.rb`         | `CreateTodolistInput`        | yes |
| Check-in question | `questions_controller.rb`         | `CreateQuestionInput`        | yes |
| Schedule entry    | `schedules/entries_controller.rb` | `CreateScheduleEntryInput`   | yes |
| Document          | `documents_controller.rb`         | `CreateDocumentInput`        | **deferred** (see below) |
| Upload            | `uploads_controller.rb`, `vaults/uploads_controller.rb` | `CreateUploadInput` | **deferred** (see below) |

**Documents and uploads are deferred at the CLI layer, not silently included.**
The CLI's `docs create` and `uploads create` (`newDocsCreateCmd` /
`newUploadsCreateCmd` in `internal/commands/files.go`) accept an arbitrary target
folder via `--vault`/`--folder`. For a **nested** vault, BC3 ignores the explicit
`visible_to_clients` param and inherits the folder's visibility — so the flag
would be a silent no-op there, which is unacceptable. Before these two commands
carry the flag, the CLI must first verify the target is the docked/root vault (or
reject the flag for nested folders). Tracked in basecamp-cli **#556**.

Note this is a **CLI-UX** constraint, not an SDK one: the SDK should still expose
`visible_to_clients` on `CreateDocumentInput` / `CreateUploadInput` for
completeness (the server accepts it), and #395 covers all six inputs. The CLI
just withholds the *flag* on those two commands until #556 adds the gating.

(The server also accepts the param for cloud files, google documents, and doors —
out of scope for #457; no CLI create commands.)

### Types that must stay unsupported

Parent-inherited recordings — create ignores the param and the toggle endpoint
403s: **Kanban cards** (inherit the card table/board), **individual todos**
(inherit the to-do list), **comments** (inherit the parent recording). The CLI
will not offer the flag on `cards create`, `todos create`, or `comments create`.

## Current SDK state (the gap)

Verified against the pinned revision **`github.com/basecamp/basecamp-sdk/go
v0.8.0`** (the version this CLI builds against). None of the six target
create-request paths carry a client-visibility field:

- Wrapper structs `CreateMessageRequest`, `CreateTodolistRequest`,
  `CreateQuestionRequest`, `CreateScheduleEntryRequest`, `CreateDocumentRequest`,
  `CreateUploadRequest` (`pkg/basecamp/*.go`) — all exist, none has the field.
- Generated `Create*RequestContent` bodies (`pkg/generated/client.gen.go`).
- Smithy `Create*Input` shapes (`spec/basecamp.smithy`).

Only `RecordingsService.SetClientVisibility` (`PUT
…/recordings/:id/client_visibility.json`) exists — a separate call against an
already-created recording. `visible_to_clients` otherwise appears only on the
**response** types (`Message`, `Card`, `Todo`, …), never on create inputs.

## Requested SDK change

For each in-scope create input, add a **tri-state** client-visibility field. Use
`*bool` end-to-end (not `bool` with `omitempty`) so the SDK can distinguish three
states: absent (`nil` → inherit from parent), explicit `true`, and explicit
`false` (team-only). `bool,omitempty` cannot represent explicit `false` — it
would be dropped from the body and silently inherit.

This mirrors the SDK's existing precedent for `AllDay` on the schedule-entry
request structs in `pkg/basecamp/schedules.go` (`AllDay *bool
\`json:"all_day,omitempty"\``; see `TestSchedulesService_UpdateEntryAllDay`,
which asserts that setting it to `false` sends `false` rather than omitting it).

Apply to **all six** server-supported inputs (SDK completeness — the CLI's
deferral of the document/upload *flag* per #556 is a CLI-UX concern that must not
narrow the SDK):

1. **Smithy** (`spec/basecamp.smithy`): add an optional `visible_to_clients:
   Boolean` member to `CreateMessageInput`, `CreateTodolistInput`,
   `CreateQuestionInput`, `CreateScheduleEntryInput`, `CreateDocumentInput`, and
   `CreateUploadInput`. Top-level body field.
2. **Regenerate** → `*bool` `VisibleToClients` on the corresponding
   `Create*RequestContent` types in `pkg/generated`.
3. **Wrapper structs** (`pkg/basecamp/*.go`): add `VisibleToClients *bool
   \`json:"visible_to_clients,omitempty"\`` to `CreateMessageRequest`,
   `CreateTodolistRequest`, `CreateQuestionRequest`, `CreateScheduleEntryRequest`,
   `CreateDocumentRequest`, and `CreateUploadRequest`.
4. **Mapping**: pass the pointer through in each `Create` method where the
   wrapper is mapped into `generated.Create*JSONRequestBody{…}`.
5. **Tests**: cover `nil` (field omitted from body), `true` (`"visible_to_clients":
   true`), and `false` (`"visible_to_clients": false` — present, not dropped).

Messages is the priority (the issue's core use case); todolists, check-in
questions, and schedule entries can land in the same SDK change or follow.
Documents and uploads are in scope for the SDK (completeness) even though the CLI
defers their flag — see #556.

**#457 closure (locked):** #457 completes only when all four initial commands —
messages, todolists, check-in questions, schedule entries — are wired. If the
messages SDK input lands first, shipping a messages-only PR is fine, but it keeps
`Refs #457` and leaves #457 **open**; the switch to `Fixes #457` happens only once
all four are wired. This fixes a stable completion boundary rather than deciding
it under merge pressure. Docs/uploads remain a separate follow-up (#556) and are
not part of the #457 boundary.

## CLI wiring that follows (after SDK bump)

Once the SDK exposes the field and the CLI bumps to it (`make bump-sdk`), each
in-scope create command gains an optional `--visible-to-clients` bool. Set the
pointer **only when the flag was provided**, so the default stays
inherit-from-parent:

```go
if cmd.Flags().Changed("visible-to-clients") {
    req.VisibleToClients = &visibleToClients
}
```

(The gate-on-`Changed` pattern matches `--subscribe` in
`internal/commands/messages.go`; the `*bool` tri-state matches `AllDay`.) A
single atomic create call — no follow-up, no partial-success path, no 403
handling, since the unsupported types don't get the flag. Then: tests asserting
the create body carries `visible_to_clients` for nil/true/false, regenerate
`.surface`, update `skills/basecamp/SKILL.md`, `bin/ci` green. Delete this file in
that commit and flip this PR to ready (`Refs #457` → `Fixes #457`).
