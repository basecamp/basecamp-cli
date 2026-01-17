# bcq Installation Guide

## Objective

Install the bcq CLI and authenticate with Basecamp. Optionally install the Claude Code plugin for enhanced AI agent workflows.

## Completion Criteria

- [ ] `bcq --version` returns version number
- [ ] `bcq auth status` shows authenticated
- [ ] `bcq projects` lists your Basecamp projects

## Prerequisites

| Requirement | Check Command | Install |
|-------------|---------------|---------|
| bash 4.0+ | `bash --version` | macOS: `brew install bash` |
| curl | `curl --version` | Usually pre-installed |
| jq | `jq --version` | `brew install jq` or `apt install jq` |
| git | `git --version` | `brew install git` or `apt install git` |

## Installation

### Step 1: Install bcq CLI

**Option A: Installer script (recommended)**

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

**Option B: Manual install**

```bash
git clone --depth 1 https://github.com/basecamp/bcq ~/.local/share/bcq
mkdir -p ~/.local/bin
ln -sf ~/.local/share/bcq/bin/bcq ~/.local/bin/bcq
```

**Add to PATH** (if not already):

```bash
# For zsh (default on macOS)
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc

# For bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

**Verify installation:**

```bash
bcq --version
# Expected: bcq 0.1.0
```

### Step 2: Authenticate with Basecamp

```bash
bcq auth login
```

This opens your browser for OAuth. Grant access when prompted.

**Verify authentication:**

```bash
bcq auth status
# Expected: Authenticated as your@email.com
```

### Step 3: Test API Access

```bash
bcq projects
```

You should see a list of your Basecamp projects.

### Step 4: Install Claude Code Plugin (Optional)

If you use Claude Code:

```bash
claude plugins install github:basecamp/bcq
```

This adds:
- `/basecamp` command for project workflows
- Automatic Basecamp context in sessions
- Commit-to-todo linking suggestions

## Configuration

### Project-Level Config

Link a repo to a Basecamp project:

```bash
cd your-project
bcq config init
bcq config set project_id 12345
bcq config set todolist_id 67890
```

Now `bcq todos` works without `--project` flag.

### Config Locations

| Scope | Path | Purpose |
|-------|------|---------|
| Project | `.basecamp/config.json` | Per-repo defaults |
| User | `~/.config/basecamp/config.json` | Global defaults |

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `BASECAMP_TOKEN` | Access token (overrides stored auth) |
| `BASECAMP_ACCOUNT_ID` | Default account ID |
| `BCQ_CACHE_ENABLED` | Enable/disable HTTP caching (default: true) |

## Updating

**bcq CLI:**

```bash
bcq self-update
```

Or manually:

```bash
cd ~/.local/share/bcq && git pull
```

**Claude Code plugin:**

```bash
claude plugin update basecamp
```

## Common Commands

```bash
# List projects
bcq projects

# List todos in a project
bcq todos --project PROJECT_ID

# Create a todo
bcq todo "Task description" --project PROJECT_ID --todolist TODOLIST_ID

# Complete a todo
bcq done TODO_ID

# Add a comment
bcq comment "Comment text" --on RECORDING_ID

# Search across projects
bcq search "keyword"
```

## Troubleshooting

### "bcq: command not found"

PATH not set. Run:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Add to shell config to persist.

### "Permission denied: read-only token"

Re-authenticate with write access:

```bash
bcq auth login --scope full
```

### "401 Unauthorized"

Token expired. Re-authenticate:

```bash
bcq auth login
```

### "No account found"

After auth, set your account:

```bash
bcq auth status  # Shows available accounts
bcq config set account_id YOUR_ACCOUNT_ID
```

### bash version too old (macOS)

macOS ships with bash 3.2. Install bash 4+:

```bash
brew install bash
```

Ensure Homebrew's bash is found first (add to `~/.zshrc` or `~/.bash_profile`):

```bash
export PATH="/opt/homebrew/bin:$PATH"
```

Verify:

```bash
bash --version
# Should show 5.x, not 3.2
```

## Uninstalling

```bash
# Remove CLI
rm -rf ~/.local/share/bcq
rm -f ~/.local/bin/bcq

# Remove config (optional)
rm -rf ~/.config/basecamp

# Remove Claude plugin (if installed)
claude plugins uninstall basecamp
```

## Quick Reference

| Action | Command |
|--------|---------|
| Install | `curl -fsSL .../install.sh \| bash` |
| Authenticate | `bcq auth login` |
| Check status | `bcq auth status` |
| List projects | `bcq projects` |
| Update CLI | `bcq self-update` |
| Update plugin | `claude plugin update basecamp` |

## Resources

- Repository: https://github.com/basecamp/bcq
- Basecamp API docs: https://github.com/basecamp/bc3-api
- Issues: https://github.com/basecamp/bcq/issues

---

**EXECUTE NOW**: Run the installation steps above. Start with `curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash`, then `bcq auth login`.
