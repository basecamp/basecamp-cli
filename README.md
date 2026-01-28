# bcq

[![CI](https://github.com/basecamp/bcq/actions/workflows/test.yml/badge.svg)](https://github.com/basecamp/bcq/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/basecamp/bcq)](https://goreportcard.com/report/github.com/basecamp/bcq)
[![Release](https://img.shields.io/github/v/release/basecamp/bcq)](https://github.com/basecamp/bcq/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE.md)

**Basecamp automation for agents, skills, MCPs, and plugins.**

- Stable command grammar for agent workflows
- JSON envelope with breadcrumbs for navigation
- Pagination, backoff, and auth handled automatically

## Quick Start

### 1. Install bcq CLI

**Homebrew (macOS/Linux):**
```bash
brew install basecamp/tap/bcq
```

**Scoop (Windows):**
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install bcq
```

**Go install:**
```bash
go install github.com/basecamp/bcq/cmd/bcq@latest
```

**Shell script:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

Then authenticate:
```bash
bcq auth login
```

### 2. Install Skills

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```

Skills install to `$BCQ_DIR` (default: `~/.local/share/bcq`).

### 3. Connect Your Agent

Skills work with any agent that can execute shell commands. Platform-specific setup below.

---

## Platform Setup

### Claude Code

```bash
claude plugin marketplace add basecamp/bcq
claude plugin install basecamp
```

This adds `/basecamp` slash command, hooks, and agents. Skills are bundled.

### Codex (OpenAI)

```bash
./scripts/install-codex.sh
```

Or manually:
1. Link skills: `ln -s ~/.local/share/bcq/skills ~/.codex/skills/bcq`
2. Reference in `~/.codex/AGENTS.md`:
   ```markdown
   @~/.codex/skills/bcq/basecamp/SKILL.md
   ```

### OpenCode

```bash
./scripts/install-opencode.sh
```

Or manually:
1. Link skills: `ln -s ~/.local/share/bcq/skills ~/.config/opencode/skill/bcq`
2. Copy agent: `cp templates/opencode/basecamp.md ~/.config/opencode/agent/`

### Gemini

Copy the template and customize:
```bash
cp templates/gemini/GEMINI.md ~/GEMINI.md
```

The template includes skill references and common bcq commands.

### GitHub Copilot

Copy the template to your repo:
```bash
cp templates/copilot/copilot-instructions.md .github/
```

The template includes skill references and code-to-Basecamp linking patterns.

### Any Other Agent

Skills are plain Markdown with bash commands. Point your agent at:
- `~/.local/share/bcq/skills/basecamp/SKILL.md` - Workflow commands
- `~/.local/share/bcq/skills/basecamp-api-reference/SKILL.md` - API reference

---

## Skills

| Skill | Purpose |
|-------|---------|
| `basecamp` | Todos, projects, team coordination |
| `basecamp-api-reference` | API endpoint lookup |

Skills use standard `Bash` tool calls — compatible with any agent.

## Update Skills

```bash
cd ~/.local/share/bcq && git pull
```

---

## Human CLI Usage

The CLI is what agents use. Humans can use it directly too.

```bash
bcq                              # Orient: context, recent activity
bcq projects                     # List projects
bcq todos                        # Your assigned todos
bcq todos --project 12345        # Todos in a project
bcq todo "Fix the bug" --project 12345  # Create todo
bcq done 67890                   # Complete todo
bcq search "authentication"      # Search across projects
```

### Output Modes

```bash
bcq projects              # Markdown when TTY, JSON when piped
bcq projects --json       # Force JSON envelope
bcq projects --quiet      # Raw JSON data only
bcq projects --stats      # Show session stats (styled/Markdown + JSON meta)
bcq projects -v           # Trace SDK operations
bcq projects -vv          # Trace operations + HTTP requests
```

Notes:
- `--stats` adds `meta.stats` to JSON output and renders a one-line summary in styled/Markdown output.
- Stats are not shown in machine modes (`--quiet`, `--agent`, `--ids-only`, `--count`).

### JSON Envelope

```json
{
  "ok": true,
  "data": [...],
  "summary": "5 projects",
  "breadcrumbs": [{"action": "show", "cmd": "bcq show project <id>"}],
  "context": {...}
}
```

## Authentication

OAuth 2.1 with Dynamic Client Registration. First login opens browser.

```bash
bcq auth login              # Full read/write access
bcq auth login --scope read # Read-only access
bcq auth login --no-browser # Headless mode
```

## Configuration

```
~/.config/basecamp/
├── credentials.json   # OAuth tokens
├── client.json        # DCR registration
└── config.json        # Preferences

.basecamp/
└── config.json        # Per-repo overrides
```

## Install Paths

| Component | Default Location | Override |
|-----------|------------------|----------|
| Repository | `~/.local/share/bcq` | `BCQ_DIR` |
| Binary | `~/.local/bin/bcq` | `BCQ_BIN_DIR` |

Skills are at `~/.local/share/bcq/skills/`. Update with `cd ~/.local/share/bcq && git pull`.

## Development

```bash
make build            # Build binary
make test             # Run Go tests
make test-bats        # Run integration tests
make lint             # Run linter
make check            # All checks (fmt, vet, test)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup.

## License

[MIT](LICENSE.md)
