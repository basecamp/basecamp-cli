# SDK Gap: create-time `visible_to_clients` (CLI issue #457)

**Status:** blocks basecamp-cli #457 — "Expose client visibility on
recording-creating commands."

**Lane note:** filed from the CLI repo per the CLI/SDK boundary. The SDK change
itself must be made in [`basecamp/basecamp-sdk`](https://github.com/basecamp/basecamp-sdk);
this file is the communiqué describing what the CLI needs.

## What the CLI needs

A way to set a recording's client visibility **at create time**, in the same
POST that creates the record, via the Go SDK's create-request types — so
`basecamp messages create --visible-to-clients` is a single atomic call rather
than a create-then-toggle follow-up.

## Server already supports it

Basecamp (bc3 **master**) accepts client visibility at create time as a
**top-level** boolean POST param `visible_to_clients`, a sibling of the
recordable payload:

```json
POST /buckets/:bucket/messages.json
{ "message": { "subject": "…", "content": "…" }, "visible_to_clients": true }
```

Implemented by the `Recording::VisibleToClientsParam` controller concern
(`app/controllers/concerns/recording/visible_to_clients_param.rb`). Semantics:
- Omitted → inherits the parent recording's visibility (falls back to `false`).
- Client users are always forced `true`.
- Documented public API section (`doc/api/sections/client_visibility.md`) only
  covers the separate toggle endpoint; the create-time param is implemented but
  undocumented there.

### Recording types that accept it at create (in scope)

These docked/top-level recordings accept `visible_to_clients` at create and have
a corresponding CLI create command:

| Recording      | Controller                                   | SDK create input        |
|----------------|----------------------------------------------|-------------------------|
| Message        | `messages_controller.rb`                     | `CreateMessageInput`      |
| Todolist       | `todolists_controller.rb`                    | `CreateTodolistInput`     |
| Document       | `documents_controller.rb`                    | `CreateDocumentInput`     |
| Check-in question | `questions_controller.rb`                 | `CreateQuestionInput`     |
| Upload         | `uploads_controller.rb` / `vaults/uploads_controller.rb` | `CreateUploadInput` |
| Schedule entry | `schedules/entries_controller.rb`            | `CreateScheduleEntryInput` |

(Server also accepts it for cloud files, google documents, and doors — out of
scope for #457 unless the CLI grows create commands for them.)

### Types that do NOT accept it (must stay unsupported)

Parent-inherited recordings — the create endpoints ignore the param and the
toggle endpoint 403s: **Kanban cards** (inherit the card table/board),
**individual todos** (inherit the to-do list), **comments** (inherit the parent
recording). The CLI will not offer the flag on `cards create`, `todos create`,
or `comments create`.

## Current SDK state (gap)

As of `github.com/basecamp/basecamp-sdk/go@v0.8.0` (and every branch/commit
checked), no create-request type carries a client-visibility field:

- Wrapper structs `CreateMessageRequest`, `CreateCardRequest`,
  `CreateTodoRequest`, `CreateCommentRequest` (`pkg/basecamp/*.go`) — none have
  `VisibleToClients`.
- Generated `Create*RequestContent` bodies (`pkg/generated/client.gen.go`) —
  none have `visible_to_clients`.
- Smithy `Create*Input` shapes (`spec/basecamp.smithy`) — none declare it.

Only `SetClientVisibility` exists (`RecordingsService.SetClientVisibility`,
`PUT …/recordings/:id/client_visibility.json`) — a separate call against an
already-created recording. `visible_to_clients` currently appears only on the
**response** types (`Message`, `Card`, `Todo`, …), never on create inputs.

## Requested SDK change

For each in-scope create input, add a `visible_to_clients` boolean (optional /
`omitempty` so omitting it preserves the server's inherit-from-parent default —
do **not** send `false` by default):

1. **Smithy** (`spec/basecamp.smithy`): add an optional `visible_to_clients:
   Boolean` member to `CreateMessageInput`, `CreateTodolistInput`,
   `CreateDocumentInput`, `CreateQuestionInput`, `CreateUploadInput`,
   `CreateScheduleEntryInput`. It is a top-level body field (a sibling of the
   nested recordable payload — Rails ParamsWrapper keeps the top-level key
   readable, matching how the server reads `params[:visible_to_clients]`).
2. **Regenerate** → adds `VisibleToClients *bool` (or `bool,omitempty`) to the
   corresponding `Create*RequestContent` types in `pkg/generated`.
3. **Wrapper structs** (`pkg/basecamp/*.go`): add `VisibleToClients bool
   \`json:"visible_to_clients,omitempty"\`` to `CreateMessageRequest` and the
   other in-scope `Create*Request` structs.
4. **Mapping**: copy the field through in each `Create` method where the wrapper
   is mapped into `generated.Create*JSONRequestBody{…}`.

Messages is the priority (the issue's core use case); the other five can land in
the same SDK change or follow.

## CLI wiring that follows (after SDK bump)

Once the SDK exposes the field and the CLI bumps to it (`make bump-sdk`), each
in-scope create command adds an optional `--visible-to-clients` bool that sets
`req.VisibleToClients` **only when** `cmd.Flags().Changed("visible-to-clients")`
(mirrors the existing `--draft` / `--subscribe` optional-flag pattern in
`internal/commands/messages.go`). No follow-up call, no partial-success path, no
403 handling — the unsupported types simply don't get the flag. Then: tests
asserting the create body carries `visible_to_clients`, regenerate `.surface`,
update `skills/basecamp/SKILL.md`, `bin/ci` green.
