# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities to **security@37signals.com**.

Do **NOT** open public GitHub issues for security vulnerabilities.

We will acknowledge receipt within 48 hours and aim to provide a fix within 90 days depending on severity.

## Credential Storage

bcq stores OAuth tokens securely using your operating system's native credential storage:

| Platform | Storage |
|----------|---------|
| macOS | Keychain |
| Windows | Credential Manager |
| Linux | Secret Service (GNOME Keyring, KWallet) |

### File-based Fallback

If system keyring is unavailable (headless servers, containers), set:

```bash
export BCQ_NO_KEYRING=1
```

Credentials will be stored in `~/.config/basecamp/credentials.json` with `0600` permissions.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| < Latest | No       |

We only provide security fixes for the latest release. Users should upgrade promptly.
