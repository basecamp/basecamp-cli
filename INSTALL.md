# bcq Installation Guide

## Objective

Enable AI agents to interact with Basecamp via bcq skills.

## Completion Criteria

- [ ] `bcq --version` returns version number
- [ ] `bcq auth status` shows authenticated
- [ ] Skills available at `~/.local/share/bcq/.claude-plugin/skills/`

## Prerequisites

| Requirement | Check Command | Install |
|-------------|---------------|---------|
| bash 4.0+ | `bash --version` | macOS: `brew install bash` |
| curl | `curl --version` | Usually pre-installed |
| jq | `jq --version` | `brew install jq` or `apt install jq` |
| git | `git --version` | `brew install git` or `apt install git` |

## Installation

### Step 1: Install bcq

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

This installs to `~/.local/share/bcq` and symlinks `bcq` to `~/.local/bin/`.

**Verify:**
```bash
bcq --version
```

### Step 2: Authenticate

```bash
bcq auth login
```

Opens browser for OAuth. Grant access when prompted.

**Verify:**
```bash
bcq auth status
# Expected: Authenticated as your@email.com
```

### Step 3: Verify Skills

Skills are installed with bcq at:
```
~/.local/share/bcq/.claude-plugin/skills/
├── basecamp/SKILL.md           # Main workflow skill
└── basecamp-api-reference/SKILL.md  # API docs lookup
```

**Test API access:**
```bash
bcq projects
```

## Using Skills

### Any Agent (Claude, Codex, OpenCode, etc.)

Load skill content from the installed path:
```
~/.local/share/bcq/.claude-plugin/skills/<skill>/SKILL.md
```

Skills use standard `Bash` tool — compatible with any agent that can run shell commands.

### Claude Code (Optional)

For tighter integration, install the plugin:
```bash
claude plugins install github:basecamp/bcq
```

This adds `/basecamp` command and automatic context loading.

## Updating

```bash
bcq self-update
```

Or re-run the installer. Skills update alongside the CLI.

## Configuration

### Per-Repo Config

Link a repo to a Basecamp project:

```bash
cd your-project
bcq config init
bcq config set project_id 12345
```

Now `bcq todos` works without `--project` flag.

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `BASECAMP_TOKEN` | Access token (overrides stored auth) |
| `BASECAMP_ACCOUNT_ID` | Default account ID |

## Troubleshooting

### "bcq: command not found"

Add to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
```

Add to shell config to persist.

### "401 Unauthorized"

Token expired. Re-authenticate:
```bash
bcq auth login
```

### bash version too old (macOS)

macOS ships with bash 3.2. Install bash 4+ via Homebrew:
```bash
brew install bash
```

## Common Commands

```bash
bcq projects                     # List projects
bcq todos                        # Your todos
bcq todos --project PROJECT_ID   # Todos in project
bcq todo "Task" --project ID     # Create todo
bcq done TODO_ID                 # Complete todo
bcq search "keyword"             # Search across projects
```

---

**EXECUTE NOW**: Run `curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash`, then `bcq auth login`.
