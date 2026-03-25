# Communique: Inline Attachment Metadata in Recording API Responses

**From:** Basecamp CLI team
**To:** BC3 Rails development
**Re:** Surfacing `<bc-attachment>` metadata as structured data in API responses
**Context:** CLI PR #326 — `basecamp attachments download`

---

## The problem

The CLI needs to let agents and humans download inline file attachments
(images, PDFs, etc.) embedded in messages, todos, cards, and documents.
Today the only way to discover these attachments is to parse the HTML body
and regex out `<bc-attachment>` elements:

```html
<bc-attachment sgid="BAh7CEk"
  content-type="application/pdf"
  href="https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf"
  filename="report.pdf"
  filesize="12345">
</bc-attachment>
```

This works, but it's fragile client-side work that every API consumer has
to replicate independently — and it depends on Trix/Basecamp internal HTML
conventions that aren't part of the documented API contract.

## What we're doing now (client-side)

The CLI's `richtext.ExtractAttachments(html)` function:

1. Regex-scans for `<bc-attachment ...>` opening tags
2. Skips mentions (`content-type="application/vnd.basecamp.mention"`)
3. Skips tags without `href` (not downloadable)
4. Extracts `href`, `filename`, `filesize`, `content-type`, `sgid`

This produces an `[]InlineAttachment` array that the CLI surfaces as an
`inline_attachments` field in show command responses and uses as the
download manifest for `basecamp attachments download`.

### Pain points

**Content field ambiguity.** The rich-text body lives in different fields
depending on recording type:

| Type     | Plain-text field | Rich HTML field |
|----------|-----------------|-----------------|
| Todo     | `content`       | `description`   |
| Message  | `subject`       | `content`       |
| Card     | —               | `content`       |
| Document | `title`         | `content`       |

The CLI has to sniff both `content` and `description`, check which one
contains HTML, and pick the right one. This is the kind of thing that
breaks silently.

**Storage URL opacity.** The `href` values in `<bc-attachment>` tags are
storage URLs (`https://storage.3.basecamp.com/...`). The SDK rewrites
these through the API host for auth, then follows a redirect to a signed
S3 URL. This two-hop dance works, but the URLs aren't guaranteed stable —
they're an implementation detail of how Trix stores blob references.

**No discoverability.** An API consumer can't tell whether a recording has
inline attachments without fetching and parsing the full HTML body. For
agents that want to decide whether to download images for multimodal
analysis, this is a wasted round-trip.

## Proposed API enhancement

Add an `inline_attachments` array to recording responses that contain rich
text. Return it alongside the existing `content`/`description` fields.

```json
{
  "id": 789,
  "type": "Message",
  "subject": "Q4 Report",
  "content": "<p>See attached: <bc-attachment ...>report.pdf</bc-attachment></p>",
  "inline_attachments": [
    {
      "sgid": "BAh7CEk",
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "byte_size": 12345,
      "download_url": "https://3.basecampapi.com/123/blobs/abc/download/report.pdf"
    }
  ]
}
```

Fields:

- **`sgid`** — the signed global ID (already in the HTML, used for
  ActionText references)
- **`filename`** — original upload filename
- **`content_type`** — MIME type
- **`byte_size`** — integer, not string (the HTML `filesize` attribute is
  a string today)
- **`download_url`** — a stable API-routable URL that the client can GET
  with auth headers, rather than a raw storage URL that requires
  rewriting. Ideally the same URL shape that `Upload#download_url` already
  returns.

### Scope

Only file attachments. Mentions (`application/vnd.basecamp.mention`) are
excluded. Tags without a downloadable blob reference are excluded.

### Which recording types

Any type whose API response includes a rich-text HTML body:

- `Message` (field: `content`)
- `Todo` (field: `description`)
- `Kanban::Card` (field: `content`)
- `Document` (field: `content`)
- `Comment` (field: `content`)
- `Question::Answer` (field: `content`)

The field would appear only when attachments are present (empty array or
omitted when none).

## What this unblocks

- **CLI:** Drop the regex parser, use structured data, remove the
  content-vs-description sniffing
- **SDK:** Add `InlineAttachments` field to recording structs
- **Agents:** Discover attachments from list/show responses without
  parsing HTML — enables "does this message have images I should look at?"
  decisions
- **Third-party integrations:** Any API consumer that wants to mirror or
  process attachments gets a stable contract instead of HTML scraping

## Impact on existing behavior

Additive. The HTML body continues to contain `<bc-attachment>` elements
as before. The new field is additional structured metadata derived from
the same source. No breaking changes.

## Migration path

If this ships, the CLI can:

1. Check for `inline_attachments` in the API response
2. Fall back to `richtext.ExtractAttachments(html)` when absent (older
   API versions, or types not yet covered)
3. Eventually remove the regex path once the API field is universal

---

**Question for BC3:** Is this something that could be derived at the
serializer level (walking the ActionText body's `<bc-attachment>` nodes
and emitting structured metadata), or does it need deeper plumbing through
the blob/attachment infrastructure?
