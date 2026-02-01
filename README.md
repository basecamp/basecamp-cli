# <img src="assets/basecamp-logo.png" height="28" alt="Basecamp">&nbsp;&nbsp;Basecamp CLI – bcq

[![CI](https://github.com/basecamp/basecamp-cli/actions/workflows/test.yml/badge.svg)](https://github.com/basecamp/basecamp-cli/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/basecamp/basecamp-cli)](https://goreportcard.com/report/github.com/basecamp/basecamp-cli)
[![Release](https://img.shields.io/github/v/release/basecamp/basecamp-cli)](https://github.com/basecamp/basecamp-cli/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE.md)

`bcq` is the official command-line interface for Basecamp. Manage projects, todos, messages, and more from your terminal or through AI agents.

- Works standalone or with any AI agent (Claude, Codex, Copilot, Gemini)
- JSON output with breadcrumbs for easy navigation
- OAuth authentication with automatic token refresh

## Quick Start

```bash
brew install basecamp/tap/bcq
bcq auth login
```

That's it. You now have full access to Basecamp from your terminal.

<details>
<summary>Other installation methods</summary>

**Windows (Scoop):**
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install bcq
```

**Go install:**
```bash
go install github.com/basecamp/basecamp-cli/cmd/bcq@latest
```

**Shell script:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```
</details>

## Usage

```bash
bcq projects                     # List projects
bcq todos --project 12345        # Todos in a project
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

bcq works with any AI agent that can run shell commands. Install skills for enhanced workflows:

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```

Skills install to `~/.local/share/bcq/skills/`.

### Platform Setup

<details>
<summary><strong>Claude Code</strong></summary>

```bash
claude plugin marketplace add basecamp/bcq
claude plugin install basecamp
```

Adds `/basecamp` slash command, hooks, and agents with skills bundled.
</details>

<details>
<summary><strong>Codex (OpenAI)</strong></summary>

```bash
./scripts/install-codex.sh
```

Or manually link skills and reference in `~/.codex/AGENTS.md`:
```markdown
@~/.codex/skills/bcq/basecamp/SKILL.md
```
</details>

<details>
<summary><strong>OpenCode</strong></summary>

```bash
./scripts/install-opencode.sh
```
</details>

<details>
<summary><strong>Gemini / Copilot / Other</strong></summary>

Copy the appropriate template from `templates/` or point your agent at:
- `~/.local/share/bcq/skills/basecamp/SKILL.md`
- `~/.local/share/bcq/skills/basecamp-api-reference/SKILL.md`
</details>

## Configuration

```
~/.config/basecamp/
├── credentials.json   # OAuth tokens
├── client.json        # DCR registration
└── config.json        # Preferences

.basecamp/
└── config.json        # Per-repo overrides
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
