# <img src="assets/bc5-snowglobe.png" height="28" alt="Basecamp"> Basecamp CLI

`basecamp` is the official command-line interface for Basecamp. Manage projects, todos, messages, and more from your terminal or through AI agents.

- Works standalone or with any AI agent (Claude, Codex, Copilot, Gemini)
- JSON output with breadcrumbs for easy navigation
- OAuth authentication with automatic token refresh
- Includes agent skills plus native Claude Code and Codex plugins

## Quick Start

**macOS / Linux / WSL2**

```bash
curl -fsSL https://basecamp.com/install-cli | bash
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.ps1 | iex
```

On Windows 11 with Smart App Control, see [Troubleshooting](#windows-smart-app-control-and-smartscreen) if the install is blocked.

On an interactive terminal, the installer opens Basecamp setup: approve OAuth in your browser and the CLI uses the account granted by OAuth, otherwise preserves an existing account or selects the first available. It saves that account globally, clears the global project default, and connects every detected coding agent. Directory-specific and environment project settings continue to apply. Use `basecamp setup --customize` to choose those settings instead.

<details>
<summary>Other installation methods</summary>

**Brew / macOS**

```
brew install --cask basecamp/tap/basecamp-cli
```

**Arch Linux / Omarchy (AUR):**
```bash
yay -S basecamp-cli
```

**Linux (deb/rpm/apk):**
```bash
# Download from https://github.com/basecamp/basecamp-cli/releases/latest
sudo apt install ./basecamp-cli_*_linux_amd64.deb            # Debian/Ubuntu
sudo dnf install ./basecamp-cli_*_linux_amd64.rpm            # Fedora/RHEL
sudo apk add --allow-untrusted ./basecamp-cli_*_linux_amd64.apk  # Alpine
```
Arm64: substitute `arm64` for `amd64` in the filename. Verify the SHA-256 checksum from `checksums.txt` before installing unsigned Alpine packages.

**Scoop (Windows):**
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install basecamp-cli
```

**Shell script (macOS / Linux / WSL2 / Git Bash):**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
```

**Nix:**
```bash
nix profile install github:basecamp/basecamp-cli
```

**Go install:**
```bash
go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest
```

**mise:**
```bash
mise use --global github:basecamp/basecamp-cli@latest
```

**GitHub Release:** download from [Releases](https://github.com/basecamp/basecamp-cli/releases).

</details>

## Upgrading

```bash
basecamp upgrade
```

What happens depends on how the CLI was installed:

- **Installer script / tarball** (a binary under your home directory, e.g. `~/bin` or `~/.local/bin`): upgrades in place. The CLI downloads the release for your platform, verifies its Sigstore signature (the keyless `checksums.txt.bundle` published by the release pipeline, identity-pinned to the release workflow and tag) and SHA-256 checksum, swaps the executable transactionally, and confirms the installed binary reports the new version. On failure the previous binary is restored; in the worst case — restoration itself fails mid-swap — the error names the preserved backup file next to the binary so you can put it back by hand.
- **Homebrew / Scoop**: delegates to `brew upgrade --cask` / `scoop update`, then verifies the manager-installed binary actually reports the new version.
- **System packages** (apt/dnf/apk, AUR, Nix), **mise**, and **`go install` builds**: never touched. `basecamp upgrade` exits nonzero with upgrade guidance for that install method (the exact command where it can be known, e.g. mise or `go install`; otherwise which package manager to use).

`basecamp upgrade` exits 0 only when there is no update, or the update was applied *and confirmed*. Every other outcome is a structured failure (`"ok": false` in JSON) with one of these codes:

| Code | Meaning |
|---|---|
| `upgrade_required` | An update exists but the CLI won't apply it for this install method — the hint carries the right next step |
| `upgrade_incomplete` | The package manager exited 0 but the binary still reports the old version |
| `upgrade_unverified` | The upgrade may have worked, but the installed version could not be confirmed |
| `upgrade_failed` | The update check, download, signature/checksum verification, or executable swap failed — the previous binary remains installed (or the error names the preserved backup if restoration also failed) |

The install scripts verify release signatures when `cosign` is available: cosign v3 verifies the published bundle format as-is, v2.6+ is driven with `--new-bundle-format=true`, and older versions skip signature verification with a warning (SHA-256 checksums are always verified).

## First-time setup

The first interactive `basecamp` run applies the recommended setup automatically after browser approval:

- Account granted by OAuth, otherwise the existing configured account or first available account, saved globally
- No global default project; directory-specific and environment project settings continue to apply
- Every detected Claude Code or Codex integration

Run the same setup directly with `basecamp setup`. To choose the account, default project, config scope, and agent integrations, run:

```bash
basecamp setup --customize
```

## Usage

```bash
basecamp projects list                            # List projects
basecamp todos list --in 12345                    # Todos in a project
basecamp todos create "Fix bug" --in 12345        # Create todo
basecamp todos complete 67890                     # Complete todo
basecamp cards done 67890 --in 12345              # Complete card (move to Done)
basecamp search "authentication"                  # Search across projects
basecamp files list --in 12345                    # List docs & files
basecamp cards list --in 12345                    # List cards (Kanban)
basecamp chat post "Hello" --in 12345             # Post to chat
basecamp comments create 67890 "@Jane.Smith, done!"    # Comment with @mention
```

### Output Formats

```bash
basecamp projects              # Styled output in terminal, JSON when piped
basecamp projects --json       # JSON with envelope and breadcrumbs
basecamp projects --quiet      # Raw JSON data only
```

### JSON Envelope

Every command supports `--json` for structured output:

```json
{
  "ok": true,
  "data": [...],
  "summary": "5 projects",
  "breadcrumbs": [{"action": "show", "cmd": "basecamp projects show <id>"}]
}
```

Breadcrumbs suggest next commands, making it easy for humans and agents to navigate.

Errors use the same envelope with `ok: false`, a stable `code`, and `retryable`,
which says whether a retry can change the outcome:

```json
{
  "ok": false,
  "error": "Gateway error (503)",
  "code": "api_error",
  "retryable": true,
  "hint": "..."
}
```

Key order within the envelope is not part of the contract — the interactive
(TTY) path re-encodes through a map and alphabetizes keys — so match on key
names, never on position.

`retryable` is present on every error envelope — `true` when the CLI classified
the failure transient (network, timeout, rate limit, circuit open, and most
5xx/gateway responses — not all: 507 and some 500s are verdicts), `false` for a
verdict (usage, not found, auth, forbidden, validation, account limit) and for
any error nothing classified — and never on a success envelope. Key on it rather
than on the code or message when deciding whether to retry; `false` means no
known reason a retry would help, not a guarantee the failure is permanent.

## Authentication

OAuth 2.1 with automatic token refresh. First login opens your browser.
When the server advertises the OAuth device flow, login uses it
automatically: you approve a short code in the browser instead of a
redirect. Login falls back to Launchpad's authorization-code flow only when
no modern OAuth issuer is advertised for the server; once a modern issuer is
selected, login failures surface loudly rather than silently falling back.

```bash
basecamp auth login              # Authenticate with Basecamp (full access)
basecamp auth login --scope read # Read-only access (ignored by Launchpad)
basecamp auth login --scope full # Full read+write access (default; ignored by Launchpad)
basecamp auth token              # Print token for scripts
```

### Multiple Identities

Use named profiles when the same machine or agent gateway needs more than one Basecamp identity. Each profile has its own stored OAuth credentials and can be selected per command:

```bash
basecamp profile create design-agent
basecamp profile create ops-agent
basecamp --profile design-agent todo "Fix bug" --in 12345 --list 67890
```

Set a default with `basecamp profile set-default <name>`, or set `BASECAMP_PROFILE=<name>` for a process. Actions are posted as the authenticated user for the selected profile.

### Custom OAuth Credentials

To use your own OAuth app (e.g., a custom Launchpad integration):

| Variable | Purpose |
|----------|---------|
| `BASECAMP_OAUTH_CLIENT_ID` | OAuth client ID |
| `BASECAMP_OAUTH_CLIENT_SECRET` | OAuth client secret |
| `BASECAMP_OAUTH_REDIRECT_URI` | Redirect URI (must be `http://` loopback with explicit port) |

Both `BASECAMP_OAUTH_CLIENT_ID` and `BASECAMP_OAUTH_CLIENT_SECRET` must be set together.

## AI Agent Integration

`basecamp` works with any AI agent that can run shell commands.

Both plugins require the `basecamp` CLI installed and on your PATH.

The plugin hooks additionally need a CLI new enough to carry the `agent-hook`
command. If hook errors appear after installing or refreshing the plugin, the
CLI is older than the hooks: run `basecamp upgrade`, then start a new session.
Check with `basecamp agent-hook --help` — an "unknown command" reply means the
CLI needs upgrading.

**Claude Code:** `basecamp setup claude` — installs the plugin with skills, hooks, and agent workflow support.

**Codex:** `basecamp setup codex` — registers the 37signals marketplace and installs the native plugin with Basecamp skills, diagnostics, and opt-in commit-reference hooks. In Codex, review and trust the plugin hooks with `/hooks`, then start a new thread to load the skills and hooks. The plugin does not inject Basecamp context at session start; Codex selects the skill when a request is relevant or explicitly references Basecamp.

Manual Codex installation uses the same marketplace:

```bash
codex plugin marketplace add basecamp/claude-plugins
codex plugin add basecamp@37signals
```

To pick up a newer plugin version later, refresh the marketplace with
`codex plugin marketplace upgrade 37signals` (or re-run `basecamp setup codex`).

**Other agents:** Point your agent at [`skills/basecamp/SKILL.md`](skills/basecamp/SKILL.md) for Basecamp workflow coverage.

**Agent discovery:** Every command supports `--help --agent` for structured JSON output (flags, gotchas, subcommands). Use `basecamp commands --json` for the full catalog.

See [install.md](install.md) for step-by-step setup instructions.

### MCP Server

`basecamp mcp` runs an MCP (Model Context Protocol) server on stdin/stdout,
serving Basecamp as tools backed by your signed-in account — no separate
binary or extra credentials. Register it with any MCP client as a stdio
server:

```bash
claude mcp add basecamp -- basecamp mcp
```

Fifteen domain tools (`basecamp_projects`, `basecamp_todos`, `basecamp_cards`,
`basecamp_messages`, `basecamp_campfires`, `basecamp_boosts`,
`basecamp_schedules`, `basecamp_files`, `basecamp_people`,
`basecamp_automation`, `basecamp_reports`, `basecamp_everything`,
`basecamp_clientside`, `basecamp_forwards`, `basecamp_account`) cover the
Basecamp API, scoped to the configured account. Each tool takes
`{"action": "...", "params": {...}}` and serves per-action schemas through
its `describe` action.

```bash
basecamp mcp --read-only                 # serve only read-only actions
basecamp mcp --domains projects,todos    # narrow the served surface (fails closed on unknown keys)
```

## Configuration

```
~/.config/basecamp/           # Your Basecamp identity
├── credentials.json          #   OAuth tokens (fallback when keyring unavailable)
└── config.json               #   Global preferences

~/.config/basecamp/theme/     # Tool display (optional)
└── colors.toml               #   TUI color scheme

~/.cache/basecamp/            # Ephemeral tool data
├── completion.json           #   Tab completion cache
└── resilience/               #   Circuit breaker state

.basecamp/                    # Per-repo (committed to git)
└── config.json               #   Project, account defaults
```

A leftover `~/.config/basecamp/client.json` (from the removed development
client-registration flow) is obsolete and safe to delete.

## Troubleshooting

```bash
basecamp doctor              # Check CLI health and diagnose issues
basecamp doctor --verbose    # Verbose output with details
basecamp doctor --json       # Structured checks, including Claude and Codex
```

### Windows: Smart App Control and SmartScreen

Releases up to v0.8.0-rc.1 ship an unsigned `basecamp.exe`. To check whether
your installed binary is signed:

```powershell
Get-AuthenticodeSignature (Get-Command basecamp).Source
```

**Smart App Control** (Windows 11) blocks unsigned executables no matter where
they were downloaded from, and it has no per-app exceptions — this applies to
the PowerShell installer, Scoop installs, and manual downloads alike. If it
blocks an unsigned `basecamp.exe`, two options:

1. **Use WSL2 (preferred).** Install the Linux build inside WSL2 — Smart App
   Control doesn't apply there and your Windows security setup is untouched:
   `wsl --install`, then inside the WSL terminal:
   `curl -fsSL https://basecamp.com/install-cli | bash`
2. **Turn Smart App Control off** (Windows Security → App & browser control →
   Smart App Control settings) **and leave it off while using the unsigned
   build.** Because there are no per-app exceptions, turning it back on
   re-blocks `basecamp.exe` on its next run — only re-enable after upgrading
   to a signed build. Windows 11 with the March/April 2026 updates can
   re-enable Smart App Control from Windows Security without a reset; on older
   builds re-enabling requires resetting Windows, so prefer WSL2 there.

**SmartScreen** (without Smart App Control) may warn on first run of an
unrecognized executable — choose "More info" → "Run anyway" if you downloaded
the release from this repository.

## Development

```bash
make build            # Build binary
make test             # Run Go tests
make test-e2e         # Run e2e tests
make lint             # Run linter
make check            # All checks (fmt-check, vet, lint, test, test-e2e)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup.

## License

[MIT](MIT-LICENSE)
