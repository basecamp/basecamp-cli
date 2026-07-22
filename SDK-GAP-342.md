# SDK Gap: card-table wormholes (CLI issue #342)

**Status:** blocks basecamp-cli #342 — "Move a card to a different project."

**Unblocker:** [basecamp/basecamp-sdk#397](https://github.com/basecamp/basecamp-sdk/issues/397).

**Lane note:** filed from the CLI repo per the CLI/SDK boundary. The SDK change
itself must be made in [`basecamp/basecamp-sdk`](https://github.com/basecamp/basecamp-sdk);
this file is the communiqué describing what the CLI needs. It is transient and is
deleted in the commit that wires the feature.

## Root cause

There is **no native cross-project card move**.
`POST /card_tables/cards/{id}/moves.json` takes only `column_id` (+`position`) and
is scoped to a single card table; there is no recording-level transfer endpoint. The
**only** cross-project mechanism for cards is a **wormhole**, and moving through one
is **asynchronous and mints a new id** — it is *teleporting*, not *moving*.

### The async / new-id / 404 contract

Passing a wormhole's `id` as `column_id` returns `204`, then a background filing
(`Recording::Mover` = `Recording::Copier` then delete-source) copies the card's tree
into the wormhole's destination **column**, mints a **new** recording id, and deletes
the source — so the original id later **404s**.

> "The `column_id` may also be the `id` of one of the board's wormholes. The card is
> then teleported to the wormhole's destination column on another card table. The
> teleport is processed asynchronously after the `204 No Content` response, and once
> it completes the card is filed away in the destination project — subsequent
> requests for the original card will return `404 Not Found`."
> — bc3 `master`, `doc/api/sections/card_table_cards.md`, "Move a card"

`app/models/recording/mover.rb` drives it:

```ruby
def move
  copy.tap do
    source_recording.transaction do
      delete_source_drafts
      delete_source_recording   # source_recording.deleted!
    end
  end
end
# copy → Recording::Copier.new(..., exclude_events: [ :inserted, :moved_in, :publicized ],
#   copy_relays: true, include_readings: true, insertion_event: :moved_in, include_comments: true)
```

Fidelity: comments, children, readings, relays, and most events survive under **new
ids**; events `[:inserted, :moved_in, :publicized]` are excluded; insertion is
re-stamped `:moved_in`. `test/models/kanban/move_test.rb` ("move card into a wormhole
teleports it to the destination column") confirms the card stays parked in its source
column until `filing.process` runs, then:

```ruby
teleported = filing.reload.destination_recording
assert_equal recordings(:anniversary_top_priority_column), teleported.parent
assert_equal buckets(:anniversary), teleported.bucket
assert_not Recording.not_deleted.exists?(card.id)   # original id is gone → 404
```

Wormholes only reach ≤4 **preconfigured** destination columns, so they do **not**
satisfy arbitrary cross-project movement — that is a further, larger gap. This work
covers the wormhole-scoped slice; #342 stays open.

## Server is ready / the SDK is the blocker

All of the below is implemented and documented on bc3 `master`. The SDK (v0.8.0 and
current main) models neither `CardTable.Wormholes` nor a wormhole CRUD service.

### 1. Move through a wormhole — already routable

`POST /card_tables/cards/{id}/moves.json` with `{ "column_id": <id>, "position": <n> }`
→ `204`, async teleport. The existing SDK `Cards().Move` already suffices to **route**
once a wormhole id is known (the wormhole id goes in as `column_id`). No change needed
here.

### 2. Card table `wormholes[]` representation

`GET /card_tables/{id}.json` returns a `wormholes` array alongside `lists`
(`type: "Kanban::Wormhole"`). Consumer-critical fields: `id`, `type`, `title`,
`linked`, `destination_url`. But the payload also carries the **full standard
recording representation** — `status`, `created_at`/`updated_at`, `url`, `app_url`,
`bookmark_url`, `parent`, `bucket`, `creator`, `color`, `visible_to_clients` — so the
generated/wrapper `Wormhole` model must reflect the **complete public response**, not
only those five fields.

```json
"wormholes": [
  {
    "id": 1069480287,
    "status": "active",
    "visible_to_clients": false,
    "created_at": "2026-07-20T04:05:47.287Z",
    "updated_at": "2026-07-20T04:05:47.287Z",
    "title": "The Leto Locator › Card Table › Triage",
    "inherits_status": true,
    "type": "Kanban::Wormhole",
    "url": "https://3.basecampapi.com/195539477/buckets/2085958505/card_tables/1069479833.json",
    "app_url": "https://3.basecamp.com/195539477/buckets/2085958505/card_tables/1069479833",
    "parent": { "id": 1069479833, "title": "Card Table", "type": "Kanban::Board", "url": "…", "app_url": "…" },
    "bucket": { "id": 2085958505, "name": "The Leto Laptop", "type": "Project" },
    "creator": { "id": 1049715913, "name": "Victor Cooper", … },
    "color": null,
    "linked": false,
    "destination_url": null
  }
]
```

Decode subtleties:

- **`linked` (`bool`)** — `true` only while the destination column exists and the
  destination board is enabled (`app/models/kanban/wormhole.rb#linked?`).
- **`destination_url` (nullable → `*string`)** — the destination column's URL, or
  **`null`** when unlinked. This is the **only** field identifying the destination:
  the wormhole's own `url`/`app_url`/`parent` point at the **source** board where the
  wormhole record lives (in the example, project `2085958505` "The Leto Laptop"),
  while the `title` `"Project › Board › Column"` names the destination but is not a
  URL. Only people who can reach the destination see the wormhole at all; max **4**
  per board.

### 3. Wormhole CRUD

`doc/api/sections/card_table_wormholes.md`:

| Op     | Route | Body | Success | Failure |
|--------|-------|------|---------|---------|
| Create | `POST /buckets/{project}/card_tables/{card_table}/wormholes.json` | `{ "destination_recording_id": <column_id> }` | `201` + wormhole JSON | `422` at the 4-per-board limit |
| Update | `PUT /buckets/{project}/card_tables/wormholes/{wormhole}.json` | `{ "destination_recording_id": <column_id> }` | `200` + wormhole JSON | — |
| Delete | `DELETE /buckets/{project}/card_tables/wormholes/{wormhole}.json` | — | `204` | — |

Route shapes differ: **create** is board-scoped
(`.../card_tables/{card_table}/wormholes.json`); **update/delete** are
wormhole-scoped and drop the board segment
(`.../card_tables/wormholes/{wormhole}.json`). `destination_recording_id` must be the
id of a **column** on another reachable card table (server validates
`kanban_list?`).

## Required SDK change

Not just a struct plus three wrappers — the full chain (see
[basecamp-sdk#397](https://github.com/basecamp/basecamp-sdk/issues/397)):

1. **API spec / generated operations** — add the three wormhole operations with the
   exact routes/payloads above, and model the `wormholes[]` member on the card-table
   response.
2. **`Wormholes` wrapper service** — `Create` / `Update` / `Delete`.
3. **Type mappings** — decode `CardTable.Wormholes []Wormhole`, with `Wormhole`
   carrying the **full recording representation** plus `Linked bool` and **nullable**
   `DestinationURL *string` (unlinked wormholes return `null`).
4. **Tests** — round-trip decode (linked + unlinked, `destination_url` null path) and
   the three CRUD wrappers.
5. **Provenance** — bump the API revision so the CLI can pin it.

`Cards().Move` needs no change (it already routes once a wormhole id is known).

## CLI wiring once unblocked

- **`cards move <id> --to-wormhole <id|dest-col-url>`** — mutually exclusive with
  `--to` / `--on-hold` / `--position`, but **preserve the existing valid
  `--to <col> --on-hold` combo** (declared in the `agent_notes` at
  `internal/commands/cards.go:729`; exercised by `TestCardsMoveOnHoldWithNumericTo`,
  `internal/commands/cards_test.go:514`). Emits honest
  *accepted / teleporting* output: the move is async, a **new** card will appear in
  the destination project, and the original id will **404** — **no same-id
  breadcrumb**.
  - A **numeric** `--to-wormhole` id goes straight to `Cards().Move` with no
    discovery.
  - A **destination-column-URL** `--to-wormhole` must first resolve the card's actual
    **source card table**, then search its `wormholes[]` for the entry whose
    **`destination_url`** parses to that column id (the wormhole's own `url`/`app_url`
    point at the source board, so they cannot be matched against a destination URL).
- **`cards wormholes --in <proj>`** — discovery reading `wormholes[]` **via the SDK
  `CardTables().Get`** (once it decodes them — not a raw request). Honors the existing
  `--card-table` resolution and ambiguity behavior.
- **Source-board resolution (do NOT use `bucket.id` as the board):** a bucket is a
  *project*, which may hold multiple card tables. Reuse the existing correct path —
  `resolveCardTableForCard` (`internal/commands/cards.go:2291`): card → parent column
  → `CardColumns().Get` → parent board id, with the fallback membership checks.
  `bucket.id` supplies only source-**project** context.

## Scope once unblocked / next steps

1. Land [basecamp-sdk#397](https://github.com/basecamp/basecamp-sdk/issues/397).
2. `make bump-sdk`; wire `--to-wormhole` + `cards wormholes`; tests (async accepted
   output, URL→destination-column matching, numeric fast path); regenerate
   `.surface`; update `skills/basecamp/SKILL.md`; `bin/ci` green.
3. Delete this file (`SDK-GAP-342.md`).
4. Keep **`Refs #342`**, not `Fixes` — wormholes reach only ≤4 preconfigured columns,
   so arbitrary-destination cross-project movement remains a further step and #342
   stays open.
