# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities to **security@37signals.com**.

Do **NOT** open public GitHub issues for security vulnerabilities.

We will acknowledge receipt within 48 hours and aim to provide a fix within 90 days depending on severity.

## Credential Storage

The Basecamp CLI stores OAuth tokens securely using your operating system's native credential storage:

| Platform | Storage |
|----------|---------|
| macOS | Keychain |
| Windows | Credential Manager |
| Linux | Secret Service (GNOME Keyring, KWallet) |

### File-based Fallback

If system keyring is unavailable (headless servers, containers), set:

```bash
export BASECAMP_NO_KEYRING=1
```

Credentials will be stored in `~/.config/basecamp/credentials.json` with `0600` permissions
(or in the configured XDG config directory). This is plaintext storage, not encryption.
Any non-empty `BASECAMP_NO_KEYRING` value bypasses the keyring before it is probed.

On Linux, the availability probe and each later keyring operation are bounded
by 10 seconds, including desktop sessions. An initial probe timeout uses the
file fallback and prints a warning on the first credential read or write.
Existing keyring credentials are not copied to the file: a fallback file may
be absent or stale.

After a successful probe, a later timeout returns an error; it never silently
switches to plaintext or serves stale file credentials. The keyring library
cannot cancel a started operation, so a timed-out write or delete may still
complete. That store refuses further keyring operations for the rest of the
process. Do not automatically retry a write whose outcome is unknown.

On macOS and Windows, only a headless session (no terminal on any standard
stream and no GUI session) bounds the availability probe; interactive sessions
leave it unbounded so an unlock prompt is not cut off mid-answer.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| < Latest | No       |

We only provide security fixes for the latest release. Users should upgrade promptly.
