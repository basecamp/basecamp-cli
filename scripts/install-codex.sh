#!/usr/bin/env bash
# install-codex.sh - Install bcq skills and config for OpenAI Codex agent

set -euo pipefail

BCQ_DIR="${BCQ_DIR:-$HOME/.local/share/bcq}"
CODEX_DIR="$HOME/.codex"
CODEX_SKILLS_DIR="$CODEX_DIR/skills"
TEMPLATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../templates/codex" && pwd)"

if [[ ! -d "$BCQ_DIR" ]]; then
  echo "Error: bcq not found at $BCQ_DIR"
  echo "Run ./scripts/install-skills.sh first."
  exit 1
fi

echo "Setting up Codex integration..."

# 1. Link skills
mkdir -p "$CODEX_SKILLS_DIR"
if [[ -L "$CODEX_SKILLS_DIR/bcq" ]]; then
  echo "Skills already linked at $CODEX_SKILLS_DIR/bcq"
else
  ln -s "$BCQ_DIR/skills" "$CODEX_SKILLS_DIR/bcq"
  echo "Linked skills to $CODEX_SKILLS_DIR/bcq"
fi

# 2. Setup AGENTS.md
if [[ -f "$CODEX_DIR/AGENTS.md" ]]; then
  echo "AGENTS.md already exists at $CODEX_DIR/AGENTS.md"
  echo "Ensure it contains the following references:"
  echo ""
  cat "$TEMPLATE_DIR/AGENTS.md"
  echo ""
else
  cp "$TEMPLATE_DIR/AGENTS.md" "$CODEX_DIR/AGENTS.md"
  echo "Created $CODEX_DIR/AGENTS.md"
fi

echo "Codex integration installed successfully."
