# bcq

Basecamp Query Tool — an agent-first interface for the Basecamp API.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/install.sh | bash
```

This installs to `~/.local/share/bcq` and creates `~/.local/bin/bcq`. Run again to update.

**Requirements:** `bash 4+`, `curl`, `jq`, `git`

### macOS

macOS ships with bash 3.2. Install modern bash first:

```bash
brew install bash jq
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/install.sh | bash
```

### Updating

Run the installer again, or `bcq self-update`

## Quick Start

```bash
# Authenticate (opens browser)
bcq auth login

# Request read-only access (least-privilege)
bcq auth login --scope read

# Headless mode (manual code entry)
bcq auth login --no-browser

# Check auth status
bcq auth status

# Orient yourself
bcq

# List projects
bcq projects

# List your todos
bcq todos

# Create a todo
bcq todo "Fix the login bug" --project 12345

# Complete a todo
bcq done 67890
```

## Output Contract

**Default**: JSON envelope when piped, markdown when TTY.

```bash
# JSON envelope (piped or --json)
bcq projects | jq '.data[0]'

# Raw data only (--quiet or --data)
bcq projects --quiet | jq '.[0]'

# Force markdown
bcq --md projects
```

### JSON Envelope Structure

```json
{
  "ok": true,
  "data": [...],
  "summary": "5 projects",
  "breadcrumbs": [
    {"action": "show", "cmd": "bcq show project <id>"}
  ],
  "context": {...},
  "meta": {...}
}
```

## Authentication

bcq uses OAuth 2.1 with Dynamic Client Registration (DCR). On first login, it registers itself as an OAuth client and opens your browser for authorization.

**Scope options:**
- `full` (default): Read and write access to all resources
- `read`: Read-only access — cannot create, update, or delete

```bash
bcq auth login              # Full access (default)
bcq auth login --scope read # Read-only access
bcq auth status             # Shows current scope
```

When requesting `full` scope, you can downgrade to `read` on the Basecamp consent screen.

If a read-only token attempts a write operation, bcq shows a clear error:
```
Error: Permission denied: read-only token cannot perform write operations
Hint: Re-authenticate with full scope: bcq auth login --scope full
```

## Configuration

```
~/.config/basecamp/
├── config.json        # Global defaults
├── credentials.json   # OAuth tokens (0600)
├── client.json        # DCR client registration
└── accounts.json      # Discovered accounts

.basecamp/
└── config.json        # Per-directory overrides
```

Config hierarchy: global → local → environment → flags

## Environment

Point `bcq` at different Basecamp instances using `BCQ_BASE_URL`:

```bash
# Production (default)
bcq projects

# Local development
BCQ_BASE_URL=http://3.basecamp.localhost:3001 bcq auth login

# Staging/beta
BCQ_BASE_URL=https://3.staging.basecampapi.com bcq auth login
```

OAuth endpoints are discovered automatically via `.well-known/oauth-authorization-server` (RFC 8414).

## Tab Completion

```bash
# Bash (add to ~/.bashrc)
source ~/.local/share/bcq/completions/bcq.bash

# Zsh (add to ~/.zshrc)
fpath=(~/.local/share/bcq/completions $fpath)
autoload -Uz compinit && compinit
```

Provides completion for commands, subcommands, and flags.

## Claude Code Integration

bcq includes a Claude Code plugin with:

- `/basecamp` - Workflow command for todos, projects, and team coordination
- `basecamp-api-reference` - API documentation lookup
- `basecamp-navigator` agent - Cross-project search
- `context-linker` agent - Link code to Basecamp items
- Session hooks - Load project context, detect todo references in commits

Install the plugin:
```bash
claude plugins install github:basecamp/bcq
```

## Testing

```bash
./test/run.sh         # Run all tests
bats test/*.bats      # Alternative: run bats directly
```

Tests use [bats-core](https://github.com/bats-core/bats-core). Install with `apt install bats` or `brew install bats-core`.

## License

[MIT](LICENSE.md)
