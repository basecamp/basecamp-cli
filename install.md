# Basecamp CLI Installation Guide

Install the Basecamp CLI and connect it to your AI agent. Execute all steps autonomously.

The piped installer (Step 1) already installs the baseline agent skill and, on a best-effort basis, attempts to connect a single detected coding agent. The optional "Connect Your Agent" section below is for forcing a specific agent, handling multiple detected agents, or verifying the connection.

**DONE WHEN:** `basecamp --version && basecamp auth status` both succeed.

---

## TODO

- [ ] Install Basecamp CLI
- [ ] Authenticate with Basecamp
- [ ] Connect agent (optional — the piped installer already attempts one detected agent)

---

## Step 1: Install Basecamp CLI

Run the matching command for your environment:

**macOS / Linux / WSL2 / Git Bash**
```bash
curl -fsSL https://basecamp.com/install-cli | bash
```

**Windows PowerShell**
```powershell
irm https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.ps1 | iex
```

> **Note:** The install scripts auto-detect non-interactive environments (CI, piped input, coding agents) and skip the interactive setup wizard. In that case they still run `basecamp setup agents`, which installs the baseline agent skill and **attempts to connect** a single detected coding agent (best effort). If several agents are detected, or none is, only the baseline skill is installed and the per-agent commands are surfaced. Explicitly skipping the wizard with `BASECAMP_SKIP_SETUP=1` still runs `setup agents`.
>
> Choose which agent to connect with `BASECAMP_SETUP_AGENT` (`claude`, `codex`, `all`, or `none`). Set it for the interpreter, not the fetch:
> - Bash: `curl -fsSL https://basecamp.com/install-cli | BASECAMP_SETUP_AGENT=codex bash`
> - PowerShell: `$env:BASECAMP_SETUP_AGENT='codex'; irm https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.ps1 | iex`
>
> **Windows note:** if `curl` fails with a `schannel` / `CRYPT_E_NO_REVOCATION_CHECK` TLS error, prefer the PowerShell installer, Scoop, or Git Bash's `/usr/bin/curl` instead of the system `curl.exe`.
>
> **Windows 11 with Smart App Control:** releases up to v0.8.0-rc.1 ship an unsigned `basecamp.exe`, which Smart App Control blocks. Prefer WSL2 — run `curl -fsSL https://basecamp.com/install-cli | bash` inside a WSL terminal — or see [Troubleshooting](#troubleshooting) below.

Alternatively install manually:

### Option A: Homebrew (macOS/Linux) — Recommended
```bash
brew install --cask basecamp/tap/basecamp-cli
```

### Option B: PowerShell (Windows)
```powershell
irm https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.ps1 | iex
```

### Option C: Scoop (Windows)
```bash
scoop bucket add basecamp https://github.com/basecamp/homebrew-tap
scoop install basecamp-cli
```

### Option D: Linux package (Debian/Ubuntu, Fedora/RHEL, Alpine)
```bash
# Download the matching package from https://github.com/basecamp/basecamp-cli/releases/latest
sudo apt install ./basecamp-cli_*_linux_amd64.deb            # Debian/Ubuntu
sudo dnf install ./basecamp-cli_*_linux_amd64.rpm            # Fedora/RHEL
sudo apk add --allow-untrusted ./basecamp-cli_*_linux_amd64.apk  # Alpine
```
Arm64: substitute `arm64` for `amd64` in the filename. Verify the SHA-256 checksum from `checksums.txt` before installing unsigned Alpine packages.

### Option E: Nix
```bash
nix profile install github:basecamp/basecamp-cli
```

### Option F: Go install
```bash
go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest
```

### Option G: GitHub Release
Download the archive for your platform from [Releases](https://github.com/basecamp/basecamp-cli/releases), extract, and move `basecamp` to a directory on your PATH.

**Verify:**
```bash
basecamp --version
# Expected: basecamp version X.Y.Z
```

If `basecamp: command not found`, add it to PATH:
```bash
export PATH="$HOME/.local/bin:$PATH"   # macOS / Linux / WSL
export PATH="$HOME/bin:$PATH"          # Git Bash / Windows bash environments
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

The piped installer (Step 1) already installs the baseline skill and attempts to connect a single detected agent. Run the commands here to force a specific agent, connect a second one, or when several agents were detected and the installer connected none. Both agent plugins require the `basecamp` CLI installed above — the plugin invokes it for every Basecamp operation.

### Claude Code

```bash
basecamp setup claude
```

This registers the marketplace and installs the plugin with skills, hooks, and agent workflow support.

### Codex

```bash
basecamp setup codex
```

This installs the shared Basecamp skill, registers the 37signals Codex marketplace, and installs the native plugin. After setup, review and trust the plugin hooks with `/hooks` (Codex lists untrusted hooks but does not run them until trusted), then start a new Codex thread to load the skills and hooks.

For a manual install:

```bash
codex plugin marketplace add basecamp/claude-plugins
codex plugin add basecamp@37signals
```

To pick up a newer plugin version later, refresh with `codex plugin marketplace upgrade 37signals` (or re-run `basecamp setup codex`).

Verify either agent integration with structured diagnostics:

```bash
basecamp doctor --json
```

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

**Windows 11: Smart App Control blocks `basecamp.exe`:**

Releases up to v0.8.0-rc.1 ship an unsigned `basecamp.exe`. Smart App Control
only runs code-signed executables — regardless of download source — and has no
per-app exceptions, so it blocks the unsigned CLI at launch. Check whether an
installed binary is signed with:

```powershell
Get-AuthenticodeSignature (Get-Command basecamp).Source
```

Preferred workaround — install inside WSL2, where Smart App Control doesn't
apply and your Windows security setup is untouched:

```powershell
wsl --install
```

then inside the WSL terminal:

```bash
curl -fsSL https://basecamp.com/install-cli | bash
```

The DONE WHEN gate (`basecamp --version && basecamp auth status`) runs inside
WSL in this setup.

Alternative — turn Smart App Control off (Windows Security → App & browser
control → Smart App Control settings) and leave it off while using the
unsigned build: because there are no per-app exceptions, re-enabling it
re-blocks `basecamp.exe` on its next run. Only re-enable after upgrading to a
signed build. Windows 11 with the March/April 2026 updates can re-enable Smart
App Control from Windows Security without a reset; on older builds re-enabling
requires resetting Windows, so use WSL2 there instead.

Plain SmartScreen (Smart App Control off) may warn on first run — choose
"More info" → "Run anyway".

**Termux / Android (`SIGSYS: bad system call` on startup):**

On Termux, the prebuilt binaries and
`go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest` crash
immediately — even `basecamp --help` — with `SIGSYS: bad system call`. A transitive
dependency probes for clipboard tools in its package initializer, and Android's
seccomp policy kills the resulting `faccessat2` syscall before the program
starts. Building from source with Termux's own Go toolchain avoids the blocked
syscall:

```bash
pkg install golang git
git clone https://github.com/basecamp/basecamp-cli
cd basecamp-cli
go build -o basecamp ./cmd/basecamp   # or: make build → bin/basecamp
```

Then move the `basecamp` binary onto your PATH. Requires Go 1.26+; if your
Termux Go is an earlier patch release, lower the `go` line in `go.mod` to
match the version you have installed.
