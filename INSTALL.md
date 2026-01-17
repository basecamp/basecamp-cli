# bcq Installation Guide

## Objective

Install bcq CLI and skills for your AI agent.

## Completion Criteria

- [ ] `bcq --version` returns version number
- [ ] `bcq auth status` shows authenticated
- [ ] Skills directory exists with SKILL.md files

## Prerequisites

| Requirement | Check Command | Install |
|-------------|---------------|---------|
| bash 4.0+ | `bash --version` | macOS: `brew install bash` |
| curl | `curl --version` | Usually pre-installed |
| jq | `jq --version` | `brew install jq` or `apt install jq` |
| git | `git --version` | `brew install git` or `apt install git` |

## Step 1: Install bcq CLI

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

Installs to `$BCQ_INSTALL_DIR` (default: `~/.local/share/bcq`).

**Verify:**
```bash
bcq --version
```

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

## Step 3: Install Skills

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```

Installs to `$BCQ_DIR` (default: `~/.local/share/bcq`).

**Custom location:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash -s -- --dir ~/my-skills
```

**Verify:**
```bash
ls $BCQ_DIR/skills/*/SKILL.md
```

## Step 4: Connect Your Agent

### Claude Code

```bash
claude plugins install github:basecamp/bcq
```

Done. Plugin bundles skills, hooks, and agents.

### Codex (OpenAI)

```bash
./scripts/install-codex.sh
```

This links skills to `~/.codex/skills/bcq`.

### OpenCode

```bash
./scripts/install-opencode.sh
```

This links skills and installs the Basecamp agent.

### Gemini

1. Copy template: `cp templates/gemini/GEMINI.md ~/GEMINI.md`
2. Template includes skill references and common commands

### GitHub Copilot

1. Copy template: `cp templates/copilot/copilot-instructions.md .github/`
2. Edit to include skill references

### Any Other Agent

Point your agent at these skill files:
```
~/.local/share/bcq/skills/
├── basecamp/SKILL.md           # Workflow skill
└── basecamp-api-reference/SKILL.md  # API docs
```

Skills use standard `Bash` tool calls.

## Updating

**CLI:**
```bash
bcq self-update
```

**Skills:**
```bash
cd ~/.local/share/bcq && git pull
```

## Troubleshooting

### "bcq: command not found"

Add to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
```

### "401 Unauthorized"

Token expired:
```bash
bcq auth login
```

### bash too old (macOS)

Install bash 4+:
```bash
brew install bash
```

## Install Paths

| Component | Default | Override |
|-----------|---------|----------|
| Repository | `~/.local/share/bcq` | `BCQ_DIR` |
| Binary | `~/.local/bin/bcq` | `BCQ_BIN_DIR` |

Skills are at `~/.local/share/bcq/skills/`.

---

**EXECUTE NOW:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
bcq auth login
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```
