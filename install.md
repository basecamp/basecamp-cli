# Basecamp CLI Installation Guide

Install the Basecamp CLI and connect it to your AI agent. Execute all steps autonomously.

**DONE WHEN:** `basecamp --version && basecamp auth status` both succeed.

---

## TODO

- [ ] Install Basecamp CLI
- [ ] Authenticate with Basecamp
- [ ] Connect agent (optional)

---

## Step 1: Install Basecamp CLI

Run this in your terminal:

```bash
curl -fsSL https://basecamp.com/install-cli | bash
```

> **Note:** The install script auto-detects non-interactive environments (CI, piped input, coding agents) and skips the interactive setup wizard. You can also explicitly skip it with `BASECAMP_SKIP_SETUP=1`.

Alternatively install manually:

### Option A: Homebrew (macOS/Linux) — Recommended
```bash
brew install --cask basecamp/tap/basecamp-cli
```

### Option B: Scoop (Windows)
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install basecamp-cli
```

### Option C: Linux package (Debian/Ubuntu, Fedora/RHEL, Alpine)
```bash
# Download the matching package from https://github.com/basecamp/basecamp-cli/releases/latest
sudo apt install ./basecamp-cli_*_linux_amd64.deb            # Debian/Ubuntu
sudo dnf install ./basecamp-cli_*_linux_amd64.rpm            # Fedora/RHEL
sudo apk add --allow-untrusted ./basecamp-cli_*_linux_amd64.apk  # Alpine
```
Arm64: substitute `arm64` for `amd64` in the filename. Verify the SHA-256 checksum from `checksums.txt` before installing unsigned Alpine packages.

### Option D: Nix
```bash
nix profile install github:basecamp/basecamp-cli
```

### Option E: Go install
```bash
go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest
```

### Option F: GitHub Release
Download the archive for your platform from [Releases](https://github.com/basecamp/basecamp-cli/releases), extract, and move `basecamp` to a directory on your PATH.

**Verify:**
```bash
basecamp --version
# Expected: basecamp version X.Y.Z
```

If `basecamp: command not found`, add to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
# or for go install:
export PATH="$HOME/go/bin:$PATH"
```

---

## Step 2: Authenticate

```bash
basecamp auth login
```

Opens browser for OAuth. Grant access when prompted.

**Verify:**
```bash
basecamp auth status
# Expected: Authenticated (BC3 OAuth may show "Authenticated (scope: read)")
```

---

EXECUTE NOW: Start with Step 1. Mark TODO items complete as you go. Stop when `basecamp --version && basecamp auth status` both succeed.

---

## Optional: Connect Your Agent

**Do not execute this section unless explicitly requested.** The core installation is complete when DONE WHEN passes.

### Claude Code

```bash
basecamp setup claude
```

This registers the marketplace and installs the plugin with skills, hooks, and agent workflow support.

### Other Agents

Point your agent at the skill file for full Basecamp workflow coverage:
```
skills/basecamp/SKILL.md
```

Every command supports `--help --agent` for structured JSON discovery.

---

## Quick Test

```bash
basecamp projects --json
basecamp search "meeting" --json
```

---

## Troubleshooting

**Not authenticated:**
```bash
basecamp auth login
```

**Wrong account:**
```bash
cat ~/.config/basecamp/config.json
basecamp auth logout && basecamp auth login
```

**Permission denied (read-only, BC3 OAuth only):**
```bash
basecamp auth login --scope full
```
