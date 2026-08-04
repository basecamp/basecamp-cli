# Communique: Replacing an Upload's File Over the API

**From:** Basecamp CLI team
**To:** BC3 Rails development
**Re:** Exposing upload file replacement (new versions) as a documented API
**Context:** CLI issue [#404](https://github.com/basecamp/basecamp-cli/issues/404) — replacing a
signed release binary in place

---

## The ask in one line

Basecamp can already replace an upload's file in place, keeping the recording
id and accumulating versions. The web app does it. The API cannot, and it is
the one thing standing between a release script and a stable download URL.

## The use case

A release pipeline uploads a signed Windows `.exe` to a Docs & Files folder and
publishes the link. Next release, it needs the *same* link to serve the new
binary. Today the only API-reachable move is `POST /vaults/:id/uploads.json`,
which creates a **new** recording with a **new** id and a **new** URL. So the
folder accumulates `basecamp-v0.8.1.exe`, `basecamp-v0.8.2.exe`, … and every
previously published link points at a stale build.

What we want instead is one durable recording whose file is replaced on each
release. `GET /uploads/:id/versions.json` already exposes the *record* of that
happening — the read side of this feature is public. The write side is not.

One clarification on that endpoint, since the rest of this document leans on it:
it returns `version_events_since_publication`, which is every recording event,
not only replacements. bc3-api currently describes it as "each version event
represents a file replacement," which overstates it — the response example
below carries an `"action": "active"` event with `"status_was": "drafted"`,
a publication transition rather than a new file. File replacement is the
`blob_changed` subset, emitted by `Upload#recorded_as` → `track_blob_change`
when the new recordable's blob differs from the previous one. Whatever write
shape you pick, please don't treat the endpoint as a list of binary revisions —
it isn't one today, and a client filtering for replacements has to select
`blob_changed` itself.

## What already exists (and why it isn't reachable)

Replacement is implemented and shipping. It's a two-step flow:

**1. Stage the file** — `POST /uploads/stage` (`Uploads::StageController#create`):

```ruby
def create
  @upload = Upload.create!(file: params[:file])
  render :show
end
```

Creates an `Upload` with no recording, and renders its signed GID.

**2. Swap it in** — `POST /buckets/:bucket/uploads/:upload_id/versions`
(`Uploads::VersionsController#create`):

```ruby
def create
  Upload.transaction do
    subscribers = find_subscribers
    @upload.update! base_name: upload_params[:base_name], description: upload_params[:description]
    @recording.update! recordable: @upload
    @recording.notify subscribers, event: @recording.version_events_since_publication.last
    @recording.change_subscribers added: subscribers
  end

  redirect_to @recording
end
```

The recording keeps its id; `Upload#recorded_as` tracks a `blob_changed` event,
which is what the versions index later lists. This is precisely the behavior
#404 needs.

Four things keep it out of reach for an API client:

1. **Undocumented.** Neither route appears in
   [bc3-api](https://github.com/basecamp/bc3-api). `sections/uploads.md`
   documents `GET /uploads/2/versions.json` and stops there.
2. **No JSON response.** `create` ends in `redirect_to @recording` with no
   `respond_to`, so a `.json` request gets a `302`, not the updated upload.
3. **Form-shaped params.** It reads `params.require(:uploads).first`, i.e. an
   array of `{sgid, base_name, description}` — shaped for the composer's
   multi-file form, not for a single-resource API call.
4. **Different upload convention.** Staging takes a raw multipart
   `params[:file]`. Every documented API upload path instead goes through
   `POST /attachments.json` and carries the result as `attachable_sgid`.
   `Upload::Creation` already accepts `attachable_sgid` — but
   `UploadsController` only wires it up on `create`:

   ```ruby
   before_action :set_new_upload, only: :create

   def update
     @recording.update! recordable: @upload.changing(upload_params), status: status_param
   end

   def upload_params
     params[:upload]&.permit(:base_name, :description) || {}
   end

   def uploadable_params
     params.permit(:attachable_sgid, :file)
   end
   ```

   So an API client that PUTs `attachable_sgid` to `/uploads/2.json` gets a
   `200 OK` and **no replacement** — the parameter is silently dropped, not
   rejected. That silence is its own small problem: the caller has no way to
   tell a no-op from a success.

## Two possible shapes

**Option A — accept `attachable_sgid` on update.** Smallest surface, matches the
convention every other API upload path already uses:

```
PUT /uploads/2.json
{ "attachable_sgid": "BAh7CEk...", "base_name": "basecamp-windows-amd64" }
```

Permit `:attachable_sgid` in `upload_params` for API requests, resolve it the
way `Upload::Creation` does, and swap the recordable when present. Omitting it
keeps today's metadata-only behavior, so this is additive.

One wrinkle worth deciding deliberately: `Uploads::VersionsController#create`
does subscriber notification and `change_subscribers` work that
`UploadsController#update` does not. If replacement moves to `update`, that
notification path needs to come along, or the API's replacements will be
quieter than the web app's.

**Option B — document a JSON `POST /uploads/2/versions.json`.** Keeps
replacement where it already lives, alongside the versions index a client would
poll afterward:

```
POST /buckets/1/uploads/2/versions.json
{ "attachable_sgid": "BAh7CEk...", "base_name": "basecamp-windows-amd64" }
```

Needs a `respond_to` returning the upload JSON instead of a redirect, and a
single-object param shape next to the existing array form.

We have no stake in which. Option A is less for a client to learn; Option B
keeps the notification behavior where it is and reads more honestly as "add a
version."

## A related read-side note

`GET /uploads/:id/versions.json` returns **version events**, not uploads:

```json
[
  {
    "id": 16850997498,
    "recording_id": 10165243019,
    "action": "active",
    "details": { "status_was": "drafted" },
    "created_at": "2026-08-04T17:15:01.261Z",
    "creator": { "id": 39024674, "name": "…" }
  }
]
```

Note the `action` here is `active`, not `blob_changed` — as above, this listing
is the full event stream since publication, and replacements are one kind of
entry in it.

The documented example in `sections/uploads.md` agrees that these are events.
The OpenAPI spec behind the Go SDK does not — it models the response as
`[]Upload`, which is a separate report to the SDK team. Flagging it here only
because whichever write shape you pick, a client will want the response and the
versions index to describe the same thing. If a `blob_changed` event carried the
replaced file's `filename`, `byte_size` and `download_url`, "show me this file's
history" would become answerable in one call.

## What this unblocks

- **#404** — a release script that replaces a binary in place, so a published
  link keeps working across releases
- **CLI** — today `basecamp files versions` can only *observe* replacements made
  in the web app. If this ships, it would list `blob_changed` events the CLI
  itself caused
- **Any integration** that mirrors an external artifact into Basecamp and wants
  one stable recording instead of a growing pile of near-duplicate uploads

## Impact on existing behavior

Additive under either option. The web composer's flow is untouched; today's
`PUT /uploads/:id.json` keeps working unchanged when the new parameter is
absent.

---

**Question for BC3:** is the staged-upload requirement
(`require_staged_upload` — the replacement Upload must have no recordings yet)
essential to how versions are tracked, or an artifact of the composer flow? It
shapes whether an API client stages first or hands over an `attachable_sgid`
directly.
