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

Installs to `$BCQ_SKILLS_DIR` (default: `~/.local/share/bcq-skills`).

**Custom location:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash -s -- --dir ~/my-skills
```

**Verify:**
```bash
ls $BCQ_SKILLS_DIR/skills/*/SKILL.md
```

## Step 4: Point Agent at Skills

Skills are at:
```
$BCQ_SKILLS_DIR/skills/
├── basecamp/SKILL.md           # Workflow skill
└── basecamp-api-reference/SKILL.md  # API docs
```

Load the SKILL.md content into your agent's instruction format. Skills use standard `Bash` tool calls.

## Updating

**CLI:**
```bash
bcq self-update
```

**Skills:**
```bash
cd $BCQ_SKILLS_DIR && git pull
# or re-run installer:
./scripts/install-skills.sh --update --dir $BCQ_SKILLS_DIR
```

## Optional: Claude Code Plugin

For tighter Claude Code integration:

```bash
claude plugins install github:basecamp/bcq
```

Adds `/basecamp` command and automatic context loading.

## Optional: Human CLI Usage

Test API access:
```bash
bcq projects
```

Common commands:
```bash
bcq todos                        # Your todos
bcq todos --project PROJECT_ID   # Todos in project
bcq todo "Task" --project ID     # Create todo
bcq done TODO_ID                 # Complete todo
bcq search "keyword"             # Search
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
| CLI | `~/.local/share/bcq` | `BCQ_INSTALL_DIR` |
| Binary | `~/.local/bin/bcq` | `BCQ_BIN_DIR` |
| Skills | `~/.local/share/bcq-skills` | `BCQ_SKILLS_DIR` |

---

**EXECUTE NOW:**
```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
bcq auth login
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install-skills.sh | bash
```
