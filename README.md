# bcq

**Basecamp automation for agents, skills, MCPs, and plugins.**

- Stable command grammar for agent workflows
- JSON envelope with breadcrumbs for navigation
- Pagination, backoff, and auth handled automatically

## Agent Quickstart

### 1. Install bcq CLI

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
bcq auth login
```

### 2. Install Skills

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```

Skills install to `$BCQ_SKILLS_DIR` (default: `~/.local/share/bcq-skills`).

**Custom location:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash -s -- --dir ~/my-skills
```

### 3. Point Your Agent at Skills

| Skill | Path | Purpose |
|-------|------|---------|
| `basecamp` | `$BCQ_SKILLS_DIR/skills/basecamp/SKILL.md` | Todos, projects, team coordination |
| `basecamp-api-reference` | `$BCQ_SKILLS_DIR/skills/basecamp-api-reference/SKILL.md` | API endpoint lookup |

Skills use standard `Bash` tool calls — compatible with any agent (Claude, Codex, OpenCode, Gemini, Copilot, etc.).

### 4. Update Skills

```bash
# Re-run installer with --update
./scripts/install-skills.sh --update --dir $BCQ_SKILLS_DIR

# Or pull directly
cd $BCQ_SKILLS_DIR && git pull
```

## Claude Code Plugin (Optional)

For tighter Claude Code integration:

```bash
claude plugins install github:basecamp/bcq
```

This adds:
- `/basecamp` slash command
- Automatic skill and agent loading
- Session hooks for project context

The plugin uses the same skills — it's a convenience layer, not a separate product.

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
```

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
| CLI | `~/.local/share/bcq` | `BCQ_INSTALL_DIR` |
| Binary | `~/.local/bin/bcq` | `BCQ_BIN_DIR` |
| Skills | `~/.local/share/bcq-skills` | `BCQ_SKILLS_DIR` |

For installer-based installs, `bcq self-update` updates the CLI. For skills, re-run `install-skills.sh --update`.

## Development

```bash
./test/run.sh         # Run all tests
bats test/*.bats      # Run bats directly
```

## License

[MIT](LICENSE.md)
