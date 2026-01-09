# bcq

Basecamp Query CLI — an agent-first command-line interface for the Basecamp API.

## Install

```bash
git clone https://github.com/basecamp/bcq.git
export PATH="$PWD/bcq/bin:$PATH"
```

Requires: `bash 4+`, `curl`, `jq`

## Quick Start

```bash
# Authenticate (opens browser)
bcq auth login

# Or headless mode
bcq auth login --no-browser

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

## Configuration

```
~/.config/basecamp/
├── config.json        # Global defaults
├── credentials.json   # OAuth tokens (0600)
└── accounts.json      # Discovered accounts

.basecamp/
└── config.json        # Per-directory overrides
```

Config hierarchy: global → local → environment → flags

## Testing

```bash
bats test/*.bats
```

## License

MIT
