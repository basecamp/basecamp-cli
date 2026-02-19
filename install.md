# bcq Installation Guide

Install bcq CLI and connect it to your AI agent. Execute all steps autonomously.

**DONE WHEN:** `bcq --version && bcq auth status` both succeed.

---

## TODO

- [ ] Install bcq CLI
- [ ] Authenticate with Basecamp
- [ ] Install skills (optional)
- [ ] Connect agent (optional)

---

## Step 1: Install bcq CLI

Choose ONE installation method:

### Option A: Homebrew (macOS/Linux) - Recommended
```bash
brew install basecamp/tap/bcq
```

### Option B: Scoop (Windows)
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install bcq
```

### Option C: Go install
```bash
go install github.com/basecamp/basecamp-cli/cmd/bcq@latest
```

### Option D: Shell script
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
```

**Verify:**
```bash
bcq --version
# Expected: bcq version X.Y.Z
```

If `bcq: command not found`, add to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
# or for go install:
export PATH="$HOME/go/bin:$PATH"
```

---

## Step 2: Authenticate

```bash
bcq auth login
```

Opens browser for OAuth. Grant access when prompted.

**Verify:**
```bash
bcq auth status
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
ls ${BCQ_DIR:-$HOME/.local/share/bcq}/skills/*/SKILL.md
# Expected: List of SKILL.md files
```

---

EXECUTE NOW: Start with Step 1. Mark TODO items complete as you go. Stop when `bcq --version && bcq auth status` both succeed.

---

## Optional: Connect Your Agent

**Do not execute this section unless explicitly requested.** The core installation is complete when DONE WHEN passes.

### Claude Code

The `.claude-plugin/` is discovered automatically when you clone the repo. No extra setup needed.

### Other Agents

Point your agent at the skill file for full Basecamp workflow coverage:
```
~/.local/share/bcq/skills/basecamp/SKILL.md
```

Or if you cloned the repo:
```
skills/basecamp/SKILL.md
```

---

## Quick Test

```bash
bcq projects --json
bcq search "meeting" --json
```

---

## Troubleshooting

**Not authenticated:**
```bash
bcq auth login
```

**Wrong account:**
```bash
cat ~/.config/basecamp/config.json
bcq auth logout && bcq auth login
```

**Permission denied (read-only):**
```bash
bcq auth login --scope full
```
