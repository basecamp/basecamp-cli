# bcq

Basecamp Query — an agent-first interface for the Basecamp API.

## For Agents

bcq provides skills that work with any AI agent capable of running shell commands.

### Available Skills

| Skill | Purpose |
|-------|---------|
| `basecamp` | Workflow command for todos, projects, team coordination |
| `basecamp-api-reference` | API endpoint lookup and documentation |

### Using Skills

**Any agent** can use bcq by:
1. Installing bcq (see [Install](#install)) — this installs both CLI and skills
2. Loading skills from `~/.local/share/bcq/.claude-plugin/skills/<skill>/SKILL.md`

Skills are self-contained markdown files with instructions and allowed tools. They use standard `Bash` tool calls.

**Example skill usage:**
```
User: "Show my Basecamp todos"
Agent: [loads basecamp skill, runs `bcq todos`]
```

### Specialized Agents

| Agent | Purpose |
|-------|---------|
| `basecamp-navigator` | Cross-project search and navigation |
| `context-linker` | Link code changes to Basecamp items |

Agent definitions are in `.claude-plugin/agents/`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

Installs to `~/.local/share/bcq`, symlinks `~/.local/bin/bcq`. Run again to update.

**Requirements:** `bash 4+`, `curl`, `jq`, `git`

**macOS:** Install modern bash first: `brew install bash jq`

### Authenticate

```bash
bcq auth login          # Opens browser for OAuth
bcq auth status         # Verify authentication
```

## CLI Reference

The CLI is what agents use. Humans can use it directly too.

```bash
bcq                              # Orient: show context, recent activity
bcq projects                     # List projects
bcq todos                        # List your todos
bcq todos --project 12345        # Todos in a project
bcq todo "Fix the bug" --project 12345  # Create todo
bcq done 67890                   # Complete todo
bcq search "authentication"      # Search across projects
```

### Output Modes

```bash
bcq projects              # Markdown when TTY, JSON when piped
bcq projects --json       # Force JSON envelope
bcq projects --quiet      # Raw JSON data only (for jq)
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

OAuth 2.1 with Dynamic Client Registration. First login opens browser for authorization.

```bash
bcq auth login              # Full read/write access
bcq auth login --scope read # Read-only access
bcq auth login --no-browser # Headless mode (manual code entry)
```

## Configuration

```
~/.config/basecamp/
├── credentials.json   # OAuth tokens
├── client.json        # DCR registration
└── config.json        # Preferences

.basecamp/
└── config.json        # Per-directory overrides
```

## Platform-Specific Packaging

### Claude Code

For optimal Claude Code integration, install the plugin:

```bash
claude plugins install github:basecamp/bcq
```

This adds:
- `/basecamp` slash command
- Automatic skill and agent loading
- Session hooks for project context

### Other Agents

For Codex, OpenCode, Gemini, Copilot, etc.:
1. Install bcq (see [Install](#install)) — includes CLI and skills
2. Load skill content from `~/.local/share/bcq/.claude-plugin/skills/*/SKILL.md`
3. Update with `bcq self-update` — updates both CLI and skills

## Development

```bash
./test/run.sh         # Run all tests
bats test/*.bats      # Run bats directly
```

## License

[MIT](LICENSE.md)
