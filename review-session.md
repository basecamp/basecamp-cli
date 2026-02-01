# Review Session: BC-Attachment OAuth Download URLs Feature Request

## Artifact
- Description: Assessment of customer feature request for OAuth-compatible download URLs for embedded attachments
- Scope: API feasibility assessment for bc-attachment blob URL OAuth support
- Quality bar: Accurate technical assessment with actionable recommendation

## Reviewers
- Reviewer A: Domain expert (BC3 Rails codebase grounding) via Task agent

---

## Round 1

### Self-Review (Implementer)

After examining the BC3 codebase, here are my findings:

**H: Authentication gap is real and intentional**
- `Blobs::PreviewsController` (previews_controller.rb:35-46) explicitly overrides `request_cookie_authentication` to return 403 for cookie auth failures
- Comment says: "When cookie auth fails... we want the result to be a broken image load, not a redirect to sign in"
- Preview URLs are designed for browser embedding, not API access
- The `Blobs::BaseController` checks `@blob.visible_to?(Current.person)` - requires session-based person

**M: Download URLs DO exist and work with API tokens**
- `Blob#download_url` (blob.rb:59-61) generates `blob_download_url(data_key, filename)`
- API jbuilder `_blob.json.jbuilder:5` includes `download_url` using `blob_download_url(..., host: api_download_host)`
- `Blobs::DownloadController` (download_controller.rb:5) has `require_download_host unless: :api_request?`
- This means download URLs SHOULD work with OAuth if properly constructed

**M: SGID resolution mechanism exists but is internal**
- `Attachable.from_attachable_sgid(sgid)` in `concerns/attachable.rb:13-17` can locate records from SGID
- Uses `GlobalID::Locator.locate_signed` with `for: LOCATOR_NAME`
- This is the internal mechanism that could power a new endpoint

**L: API already exposes some attachment metadata**
- `api/attachments/_attachment.json.jbuilder` includes `attachable_sgid`, `status_url`, and partial for `blobs/blob`
- `api/blobs/_blob.json.jbuilder` includes `download_url`
- So when you fetch an attachment via API, you DO get the download URL

### External Review (BC3 Domain Expert)

**H: Critical gap - bc-attachment tags in rich text DO NOT include OAuth-compatible download URLs**
- My assessment was partially incorrect
- While `api/blobs/_blob.json.jbuilder` uses `api_download_host`, this partial is NOT used when bc-attachment tags are embedded in rich text
- `Blob#to_rich_text_attributes` generates URLs with `script_name: Current.account.slug_path` but **no explicit host**
- This generates URLs for default host (preview.3.basecamp.com), not API host

**H: Preview URLs are intentionally cookie-only - CONFIRMED**
- PreviewsController override confirmed
- Includes `UnauthenticatedImageResponse` which ensures failed auth returns 403

**M: SGID resolution endpoint is NOT usable for attached content**
- Existing `GET attachments/:sgid` returns 403 Forbidden when attachment has recordings
- Designed for upload polling, NOT for resolving embedded attachments

**M: Download URLs DO work with OAuth - CONFIRMED (partially)**
- Download controller has `require_download_host unless: :api_request?`
- OAuth-authenticated API requests CAN access blob downloads
- The issue is getting the correct URL in the first place

**L: API attachment partial includes download_url - CONFIRMED**
- When attachments returned through proper API endpoints, download_url uses api_download_host
- But doesn't help for bc-attachment tags in rich text

**Additional considerations:**
- Performance: SGID resolution would need batch support (N+1 problem)
- Authorization: Must verify requester access to parent recording
- Rate limiting needed for batch resolution

**Recommended: Option 2 (SGID resolution endpoint)**
- New endpoint: `GET /attachments/resolve.json?sgids[]=SGID1&sgids[]=SGID2`
- Supports batch resolution
- Returns full attachment JSON with OAuth-compatible download_url
- Can reuse existing `api/attachments/_attachment.json.jbuilder` partial

**Confidence: High**

### Reconciliation

| Issue | Self-Review | External | Agreement |
|-------|-------------|----------|-----------|
| Preview URLs cookie-only | Found | Found | Agree |
| Download URLs work with OAuth | Found | Confirmed w/ clarification | Agree |
| SGID resolution exists | Found (as internal mechanism) | Corrected - not usable for attached content | Disagree - my assessment missed this |
| API attachments have download_url | Found | Confirmed, but noted it doesn't help for rich text | Agree with clarification |
| **bc-attachment URLs in rich text** | **Missed** | **Found as critical gap** | New finding |

**Key correction:** I stated "API attachments partial already includes download_url via blob partial" but this is irrelevant - the real issue is that `Blob#to_rich_text_attributes` (used when embedding bc-attachment in rich text content) does NOT use the API host. The jbuilder partial is only used when you fetch an attachment directly via API, not when attachments appear in rich text bodies.

### Round 1 Synthesis

**Consensus:**
- The customer's problem is real and well-identified
- Preview URLs are intentionally cookie-only (security feature)
- Download URLs CAN work with OAuth when accessed via API host
- The gap: `Blob#to_rich_text_attributes` generates non-API URLs in bc-attachment tags
- Option 2 (SGID resolution endpoint) is the most feasible solution

**Disagreements:**
- Self-review vs External: I thought existing infrastructure was sufficient; external correctly identified that `to_rich_text_attributes` is the core problem
  - Decision: External review is correct - the existing SGID infrastructure cannot be used as-is for this use case

**Actions:**
- [x] Correct assessment about where the gap actually is
- [ ] Recommend SGID batch resolution endpoint as solution

**Decision points:** none this round (assessment task, not implementation).

**Open Questions:**
- Would the BC4 team accept an API addition for this use case?
- Should the endpoint support preview URL resolution as well, or just download URLs?

**Gate Status:**
- Open high-severity items? no (findings integrated)
- Open medium items accepted? yes
- All actions addressed? yes
- Ready to close? yes

**Mediator Approval:** pending
