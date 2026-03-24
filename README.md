# <img src="assets/basecamp-badge.svg" height="28" alt="Basecamp"> Basecamp CLI

`basecamp` is the official command-line interface for Basecamp. Manage projects, todos, messages, and more from your terminal or through AI agents.

- Works standalone or with any AI agent (Claude, Codex, Copilot, Gemini)
- JSON output with breadcrumbs for easy navigation
- OAuth authentication with automatic token refresh

## Quick Start

```bash
curl -fsSL https://basecamp.com/install-cli | bash
```

That's it. You now have full access to Basecamp from your terminal.

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

**Shell script:**
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

**GitHub Release:** download from [Releases](https://github.com/basecamp/basecamp-cli/releases).

</details>

## Usage

```bash
basecamp projects list                # List projects
basecamp todos list --in 12345        # Todos in a project
basecamp todo "Fix bug" --in 12345    # Create todo
basecamp done 67890                   # Complete todo
basecamp search "authentication"      # Search across projects
basecamp files list --in 12345        # List docs & files
basecamp cards list --in 12345        # List cards (Kanban)
basecamp chat post "Hello" --in 12345      # Post to chat
basecamp comment 67890 "@Jane.Smith, done!"    # Comment with @mention
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

## Authentication

OAuth 2.1 with automatic token refresh. First login opens your browser:

```bash
basecamp auth login              # Authenticate with Basecamp
basecamp auth login --scope read # Read-only access (BC3 OAuth only, default)
basecamp auth login --scope full # Full read+write access (BC3 OAuth only)
basecamp auth token              # Print token for scripts
```

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

**Claude Code:** `basecamp setup claude` — installs the plugin with skills, hooks, and agent workflow support.

**Other agents:** Point your agent at [`skills/basecamp/SKILL.md`](skills/basecamp/SKILL.md) for Basecamp workflow coverage.

**Agent discovery:** Every command supports `--help --agent` for structured JSON output (flags, gotchas, subcommands). Use `basecamp commands --json` for the full catalog.

See [install.md](install.md) for step-by-step setup instructions.

## Configuration

```
~/.config/basecamp/           # Your Basecamp identity
├── credentials.json          #   OAuth tokens (fallback when keyring unavailable)
├── client.json               #   DCR client registration
└── config.json               #   Global preferences

~/.config/basecamp/theme/     # Tool display (optional)
└── colors.toml               #   TUI color scheme

~/.cache/basecamp/            # Ephemeral tool data
├── completion.json           #   Tab completion cache
└── resilience/               #   Circuit breaker state

.basecamp/                    # Per-repo (committed to git)
└── config.json               #   Project, account defaults
```

## Troubleshooting

```bash
basecamp doctor              # Check CLI health and diagnose issues
basecamp doctor --verbose    # Verbose output with details
```

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
