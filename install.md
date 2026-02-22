# Basecamp CLI Installation Guide

Install the Basecamp CLI and connect it to your AI agent. Execute all steps autonomously.

**DONE WHEN:** `basecamp --version && basecamp auth status` both succeed.

---

## TODO

- [ ] Install Basecamp CLI
- [ ] Authenticate with Basecamp
- [ ] Install skills (optional)
- [ ] Connect agent (optional)

---

## Step 1: Install Basecamp CLI

Choose ONE installation method:

### Option A: Homebrew (macOS/Linux) - Recommended
```bash
brew install --cask basecamp/tap/basecamp
```

### Option B: Scoop (Windows)
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install basecamp
```

### Option C: Go install
```bash
go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest
```

### Option D: Shell script
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
```

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
# Expected: Authenticated as your@email.com
```

---

## Step 3: Install Skills (Optional)

Skills provide AI agents with Basecamp workflow knowledge.

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install-skills.sh | bash
```

**Verify:**
```bash
ls ${BASECAMP_DIR:-$HOME/.local/share/basecamp}/skills/*/SKILL.md
# Expected: List of SKILL.md files
```

---

EXECUTE NOW: Start with Step 1. Mark TODO items complete as you go. Stop when `basecamp --version && basecamp auth status` both succeed.

---

## Optional: Connect Your Agent

**Do not execute this section unless explicitly requested.** The core installation is complete when DONE WHEN passes.

### Claude Code

The `.claude-plugin/` is discovered automatically when you clone the repo. No extra setup needed.

### Other Agents

Point your agent at the skill file for full Basecamp workflow coverage:
```
~/.local/share/basecamp/skills/basecamp/SKILL.md
```

Or if you cloned the repo:
```
skills/basecamp/SKILL.md
```

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

**Permission denied (read-only):**
```bash
basecamp auth login --scope full
```
