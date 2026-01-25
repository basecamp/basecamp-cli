#!/usr/bin/env bash
# install-opencode.sh - Install bcq skills and config for OpenCode

set -euo pipefail

BCQ_DIR="${BCQ_DIR:-$HOME/.local/share/bcq}"
OPENCODE_DIR="$HOME/.config/opencode"
OPENCODE_SKILL_DIR="$OPENCODE_DIR/skill"
OPENCODE_AGENT_DIR="$OPENCODE_DIR/agent"
TEMPLATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../templates/opencode" && pwd)"

if [[ ! -d "$BCQ_DIR" ]]; then
  echo "Error: bcq not found at $BCQ_DIR"
  echo "Run ./scripts/install-skills.sh first."
  exit 1
fi

echo "Setting up OpenCode integration..."

# 1. Link skills
mkdir -p "$OPENCODE_SKILL_DIR"
if [[ -L "$OPENCODE_SKILL_DIR/bcq" ]]; then
  echo "Skills already linked at $OPENCODE_SKILL_DIR/bcq"
else
  ln -s "$BCQ_DIR/skills" "$OPENCODE_SKILL_DIR/bcq"
  echo "Linked skills to $OPENCODE_SKILL_DIR/bcq"
fi

# 2. Install Agent
mkdir -p "$OPENCODE_AGENT_DIR"
cp "$TEMPLATE_DIR/basecamp.md" "$OPENCODE_AGENT_DIR/basecamp.md"
echo "Installed agent definition to $OPENCODE_AGENT_DIR/basecamp.md"

echo "OpenCode integration installed successfully."
