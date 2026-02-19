# <img src="assets/basecamp-badge.svg" height="28" alt="Basecamp"> Basecamp CLI – bcq

> **Prerelease.** This is an early internal release for 37signals dogfooding. The repo is private — all install methods below require GitHub access. Expect rough edges; file issues as you find them.

`bcq` is the official command-line interface for Basecamp. Manage projects, todos, messages, and more from your terminal or through AI agents.

- Works standalone or with any AI agent (Claude, Codex, Copilot, Gemini)
- JSON output with breadcrumbs for easy navigation
- OAuth authentication with automatic token refresh

## Quick Start

```bash
brew install --cask basecamp/tap/bcq
bcq auth login
```

That's it. You now have full access to Basecamp from your terminal.

<details>
<summary>Other installation methods</summary>

**Go install** (requires `GOPRIVATE=github.com/basecamp/*`):
```bash
go install github.com/basecamp/basecamp-cli/cmd/bcq@latest
```

**Shell script:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
```

**Windows (Scoop):**
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install bcq
```
</details>

## Usage

```bash
bcq projects                     # List projects
bcq todos --in 12345             # Todos in a project
bcq todo --content "Fix bug" --in 12345  # Create todo
bcq done 67890                   # Complete todo
bcq search "authentication"      # Search across projects
bcq cards --in 12345             # List cards (Kanban)
bcq campfire post "Hello" --in 12345  # Post to chat
```

### Output Formats

```bash
bcq projects              # Styled output in terminal, JSON when piped
bcq projects --json       # JSON with envelope and breadcrumbs
bcq projects --quiet      # Raw JSON data only
```

### JSON Envelope

Every command supports `--json` for structured output:

```json
{
  "ok": true,
  "data": [...],
  "summary": "5 projects",
  "breadcrumbs": [{"action": "show", "cmd": "bcq show project <id>"}]
}
```

Breadcrumbs suggest next commands, making it easy for humans and agents to navigate.

## Authentication

OAuth 2.1 with automatic token refresh. First login opens your browser:

```bash
bcq auth login              # Full read/write access
bcq auth login --scope read # Read-only access
bcq auth token              # Print token for scripts
```

## AI Agent Integration

bcq works with any AI agent that can run shell commands.

**Claude Code:** The `.claude-plugin/` is discovered automatically when you clone this repo. For standalone use, point at `skills/basecamp/SKILL.md`.

**Other agents:** Point your agent at [`skills/basecamp/SKILL.md`](skills/basecamp/SKILL.md) for full Basecamp workflow coverage.

**One-liner** to install skills locally (any agent):
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install-skills.sh | bash
```

See [install.md](install.md) for step-by-step setup instructions.

## Configuration

```
~/.config/basecamp/           # Your Basecamp identity
├── credentials.json          #   OAuth tokens (fallback when keyring unavailable)
├── client.json               #   DCR client registration
└── config.json               #   Global preferences

~/.config/bcq/theme/          # Tool display (optional)
└── colors.toml               #   TUI color scheme

~/.cache/bcq/                 # Ephemeral tool data
├── completion.json           #   Tab completion cache
└── resilience/               #   Circuit breaker state

.basecamp/                    # Per-repo (committed to git)
└── config.json               #   Project, account defaults
```

## Troubleshooting

```bash
bcq doctor              # Check CLI health and diagnose issues
bcq doctor -V           # Verbose output with details
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

[MIT](LICENSE.md)
