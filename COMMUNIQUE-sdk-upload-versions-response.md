# Communique: `UploadsService.ListVersions` Decodes Version Events as Uploads

**From:** Basecamp CLI team
**To:** basecamp-sdk (Go)
**Re:** `ListUploadVersionsResponseContent` is modeled as `[]Upload`; the API
returns version events
**Affects:** SDK v0.12.0 (current pin) — `pkg/basecamp/vaults.go:956`,
`pkg/generated/client.gen.go:1625`
**Context:** CLI branch `feat/files-versions`, held rather than merged because
of this

---

## The defect

`GET /uploads/{id}/versions.json` returns a list of **version events**. The
OpenAPI spec models it as a list of **uploads**:

```go
// pkg/generated/client.gen.go:1625
type ListUploadVersionsResponseContent = []Upload
```

`UploadsService.ListVersions` then decodes each element with
`uploadFromGenerated`, so events are silently coerced into `Upload` structs.
Nothing errors — the fields simply do not line up.

## Evidence

`bc3-api/sections/uploads.md` documents the response as events, and a live
`GET /uploads/10165243019/versions.json` against production agrees:

```json
[
  {
    "id": 16850997498,
    "recording_id": 10165243019,
    "action": "active",
    "details": { "delivered_recipient_ids": [], "status_was": "drafted" },
    "created_at": "2026-08-04T17:15:01.261Z",
    "creator": { "id": 39024674, "name": "…" }
  }
]
```

The bc3 implementation confirms it —
`Uploads::VersionsController#index` renders
`@recording.version_events_since_publication.reverse_chronologically`.

## What a caller gets today

Running the same request through `ListVersions` yields an `Upload` that is
mostly empty, and worse, one that is confidently wrong:

```json
{
  "id": 16850997498,
  "created_at": "2026-08-04T17:15:01.261Z",
  "creator": { "id": 39024674, "name": "…" },
  "title": "",
  "filename": "",
  "status": "",
  "content_type": "",
  "byte_size": 0,
  "download_url": "",
  "url": "",
  "app_url": "",
  "updated_at": "0001-01-01T00:00:00Z"
}
```

Three distinct problems, in increasing order of harm:

1. **Dropped fields.** `action`, `details` and `recording_id` — the only fields
   that say *what happened* — have nowhere to land and are discarded.
2. **Empty fields that read as data.** `title`, `filename`, `status`,
   `download_url`, `byte_size` are zero values, not absent ones. A caller
   rendering a versions table gets blank cells and no signal that the blanks
   are an artifact.
3. **A misleading `id`.** `16850997498` is the *event* id. Typed as an `Upload`,
   it invites `Uploads().Get(ctx, 16850997498)`, which 404s. The real upload id
   is in the discarded `recording_id`.

## Suggested fix

Model the response as its own type rather than reusing `Upload`:

```go
// UploadVersion is one entry in an upload's change history.
type UploadVersion struct {
    ID          int64          // the event id, not an upload id
    RecordingID int64          // the upload this version belongs to
    Action      string         // e.g. "active", "blob_changed"
    Details     map[string]any
    CreatedAt   time.Time
    Creator     Person
}

type UploadVersionListResult struct {
    Versions []UploadVersion
    Meta     ListMeta
}
```

This is a breaking change to `UploadVersionListResult.Versions`, but the
current element type carries no usable data, so we do not expect real
callers to be relying on it.

The spec is the root cause — `ListUploadVersionsResponseContent = []Upload`
comes from the OpenAPI description, so the fix likely belongs upstream of the
generator, with the hand-written service following.

## What it unblocks

CLI PR for `basecamp files versions` is written, tested and complete, and is
held unmerged solely on this. As soon as `ListVersions` returns the event
shape, the command ships: `--limit`/`--page`/`--all` semantics, catalog entry,
surface snapshot and smoke coverage are already in place.

It also matters for `API-COVERAGE.md`. `ListVersions` is the one uploads
endpoint the CLI does not cover, and the reason is this type — not a missing
command.

---

**Note:** no changes were made to the SDK repository. Reporting only.
