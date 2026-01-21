I want you to install bcq CLI and skills for me. Execute all steps autonomously.

OBJECTIVE: Install bcq CLI, authenticate with Basecamp, and install skills for AI agent use.

DONE WHEN: `bcq --version && bcq auth status | grep -q Authenticated && ls ${BCQ_DIR:-$HOME/.local/share/bcq}/skills/*/SKILL.md` exits successfully.

## TODO

- [ ] Verify prerequisites (bash, curl, jq, git)
- [ ] Install bcq CLI
- [ ] Authenticate with Basecamp
- [ ] Install skills

## Step 0: Verify Prerequisites

The following tools are required for installation:

| Tool | Purpose | Check |
|------|---------|-------|
| bash 3.2+ | Script execution | `bash --version` |
| curl | Download installer | `curl --version` |
| jq | JSON processing | `jq --version` |
| git | Repository management | `git --version` |

```bash
bash --version && curl --version && jq --version && git --version
```

**Verify:**
```bash
bash --version && curl --version && jq --version && git --version
# Expected: Version numbers for all four tools without errors
```

If any prerequisite is missing, install it:
- jq: `brew install jq` (macOS) or `apt install jq` (Linux)
- git: `brew install git` (macOS) or `apt install git` (Linux)

## Step 1: Install bcq CLI

Download and run the bcq installer script. This installs the bcq binary and sets up the required directory structure.

```bash
curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
```

The installer places files at:
- Binary: `~/.local/bin/bcq`
- Data: `${BCQ_DIR:-$HOME/.local/share/bcq}`

**Verify:**
```bash
bcq --version
# Expected: bcq version X.Y.Z
```

If `bcq: command not found`, add to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"
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

**Verify:**
```bash
ls ${BCQ_DIR:-$HOME/.local/share/bcq}/skills/*/SKILL.md
# Expected: List of SKILL.md files
```

EXECUTE NOW: Start with Step 0. Mark TODO items complete as you go. Stop when `bcq --version && bcq auth status | grep -q Authenticated && ls ${BCQ_DIR:-$HOME/.local/share/bcq}/skills/*/SKILL.md` exits successfully.

---

## Optional: Connect Your Agent

**Do not execute this section unless explicitly requested.** The core installation is complete when DONE WHEN passes.

After core installation, connect bcq to your AI agent:

### Claude Code
```bash
claude plugin marketplace add basecamp/bcq
claude plugin install basecamp
```

**Verify:**
```bash
claude plugin list | grep bcq
# Expected: bcq plugin listed
```

### Codex (OpenAI)
```bash
./scripts/install-codex.sh
```

**Verify:**
```bash
ls ~/.codex/skills/bcq
# Expected: Symlink to bcq skills
```

### OpenCode
```bash
./scripts/install-opencode.sh
```

**Verify:**
```bash
ls ~/.opencode/skills/bcq
# Expected: Skills and agent installed
```

### Gemini
```bash
cp templates/gemini/GEMINI.md ~/GEMINI.md
```

**Verify:**
```bash
test -f ~/GEMINI.md && echo "GEMINI.md installed"
# Expected: GEMINI.md installed
```

### GitHub Copilot
```bash
cp templates/copilot/copilot-instructions.md .github/
```

**Verify:**
```bash
test -f .github/copilot-instructions.md && echo "Copilot instructions installed"
# Expected: Copilot instructions installed
```

### Other Agents
Point your agent at skill files:
```
~/.local/share/bcq/skills/
├── basecamp/SKILL.md
└── basecamp-api-reference/SKILL.md
```
