# Installing bcq and the Basecamp Plugin

bcq is a CLI for interacting with Basecamp, designed for use with AI coding agents like Claude Code.

## Quick Start

### 1. Install bcq CLI

```bash
# Clone and install
git clone https://github.com/basecamp/bcq ~/.local/share/bcq
ln -sf ~/.local/share/bcq/bin/bcq ~/.local/bin/bcq

# Ensure ~/.local/bin is in PATH
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc  # or ~/.zshrc
source ~/.bashrc

# Verify
bcq --version
```

Or use the installer script:
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

### 2. Authenticate with Basecamp

```bash
bcq auth login
```

This opens your browser for OAuth authentication. No tokens to copy/paste.

### 3. Install Claude Code Plugin (optional)

If you use Claude Code:

```bash
claude plugins install github:basecamp/bcq
```

This enables:
- `/basecamp` command for workflows
- Automatic context loading
- Commit-to-todo linking suggestions

## Manual Installation

### Prerequisites

- bash 4.0+
- curl
- jq
- git (for installation and updates)

### From Source

```bash
git clone https://github.com/basecamp/bcq
cd bcq

# Option A: Add bin to PATH
export PATH="$PWD/bin:$PATH"

# Option B: Symlink to standard location
ln -sf "$PWD/bin/bcq" ~/.local/bin/bcq
```

### Updating

If installed via git clone:
```bash
cd ~/.local/share/bcq  # or your install location
git pull
```

Or use:
```bash
bcq self-update
```

## Verification

```bash
# Check installation
bcq --version

# Check authentication
bcq auth status

# List projects (requires auth)
bcq projects
```

## Configuration

bcq looks for configuration in:
1. `.basecamp/config.json` in current directory (project-specific)
2. `~/.config/basecamp/config.json` (user defaults)

Set defaults for a project:
```bash
cd your-project
bcq config init
bcq config set project_id 12345
bcq config set todolist_id 67890
```

## Troubleshooting

### "bcq: command not found"

Ensure `~/.local/bin` is in your PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
```

### "Permission denied: read-only token"

Re-authenticate with full scope:
```bash
bcq auth login --scope full
```

### "No account configured"

Run setup:
```bash
bcq auth login
bcq config set account_id YOUR_ACCOUNT_ID
```

## Uninstalling

```bash
# Remove bcq
rm -rf ~/.local/share/bcq
rm ~/.local/bin/bcq

# Remove config (optional)
rm -rf ~/.config/basecamp

# Remove Claude plugin (if installed)
claude plugins uninstall basecamp
```
