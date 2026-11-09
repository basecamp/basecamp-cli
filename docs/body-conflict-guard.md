# Opt-in conflict checks for replacements

Fetch the record before preparing an edit. Keep the `updated_at` from the representation actually used, then pass it unchanged as `--if-unmodified-since <updated_at>` to the typed command. Do not fetch a new timestamp merely to make an old replacement pass.

```sh
basecamp comments show 789 --json
basecamp comments update 789 - --if-unmodified-since '2026-11-08T18:10:58.123Z' --json < revised.md
```

The baseline is optional. **Without it, including the first write from this CLI, current unprotected behavior is unchanged.** There is no automatic first-use protection, remembered read version, or local ledger.

With a baseline the CLI performs a server-revalidated GET and requires equal parsed timestamp instants, not equal text spellings. Equivalent UTC offsets compare equal; different fractional seconds or a future baseline do not. Equality is deliberately conservative: an unrelated field change can also refuse a body edit. Invalid/zero input, missing/zero/unparseable live timestamps, and failed reads refuse without uploading attachments or mutating the target. `--force` overrides only a version mismatch; it does not waive invalid input or unavailable live state. `--force` requires a baseline.

Re-fetch and reconcile on conflict. Only deliberately use `--force` with the supplied baseline when you intend to overwrite intervening changes. A creator is not necessarily the latest editor; the CLI does not invent an editor identity.

## Exact command coverage

These commands accept the opt-in, **whole-record** version check, including their non-body edits. Guard flags on a title-only edit therefore check that record's version rather than being ignored:

- `comments update`
- `messages update`
- `todos update` (description changes/clears and every other update through this command)
- `cards update` (including attachment-only replacements)
- `todolists update`
- `schedule update` (including attachment-only updates)
- `checkins answer update`
- `chat update` (including a Ping line addressed through this command)
- `files update` (document/upload/vault, explicit type or autodetection, including title-only document writes and explicit clears)
- `files replace` (including description replacements/clears and file-only replacements)
- `doors update` (including description replacements/clears)
- `projects update`
- `templates update`
- `notes set` (an unwritten note has no version and refuses a supplied baseline)
- `gauges update`
- `timesheet entry update`

The guard is **not global middleware**. Raw `api put`, MCP/TUI and SDK callers, creation commands, other commands that carry a preserved body (for example to-do assignment operations), and other update verbs do not gain protection. **Boost editing is explicitly excluded from this first opt-in safeguard.** `boost update` continues to replace content without this protection and does not accept these guard flags. SDK go/v0.35.0 exposes only `created_at`, not `updated_at`, on Boost; the API representation likewise omits `updated_at`. An ETag is a different baseline contract and is not substituted here. Do not substitute creation time or a parent recording's timestamp. Boost editing is unchanged; adding its version support is separate work. Never treat unguarded paths as protected merely because a guarded typed command exists. Use guarded typed commands for body edits rather than bypassing them with raw API writes.

## Limits

The timestamp is an API-supplied record version signal, not a cryptographic or necessarily unique per-edit token. SDK HTTP cache hits still contact the server for validation; they do not supply an acknowledged caller baseline.

**GET then PUT is not atomic.** An edit after the guard's GET can still be overwritten. Some SDK composite updates (notably to-dos and documents) perform another GET and send the full writable representation; this neither removes the race nor protects other command paths automatically. No conditional-write support or server changes are implied by the flag name. Ledger, append mode, default protection and server atomicity are separate features, not part of this change.
