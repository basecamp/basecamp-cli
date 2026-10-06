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

Credentials will be stored in `~/.config/basecamp/credentials.json` with `0600` permissions.

Don't copy that file between machines or containers. A login refreshes by
replacing its token, and Basecamp signs out every copy once two of them
refresh more than a minute apart. See "Containers, CI and scheduled jobs" in the README for what to do
instead.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| < Latest | No       |

We only provide security fixes for the latest release. Users should upgrade promptly.
