#!/usr/bin/env bash
# install-claude.sh - Install bcq plugin for Claude Code
#
# Usage:
#   ./scripts/install-claude.sh
#
# Prerequisites:
#   - Claude Code CLI (claude) installed
#   - bcq CLI installed and authenticated

set -euo pipefail

info() { echo "==> $1"; }
error() { echo "ERROR: $1" >&2; exit 1; }

# Check prerequisites
check_prereqs() {
  if ! command -v claude &>/dev/null; then
    error "Claude Code CLI not found. Install from: https://claude.ai/code"
  fi

  if ! command -v bcq &>/dev/null; then
    error "bcq CLI not found. Run: curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash"
  fi
}

install_plugin() {
  info "Installing bcq plugin for Claude Code..."

  # Add from marketplace
  if ! claude plugin marketplace add basecamp/bcq 2>/dev/null; then
    info "Plugin may already be in marketplace, continuing..."
  fi

  # Install the plugin
  claude plugin install basecamp

  info "Plugin installed successfully"
}

verify_install() {
  if claude plugin list 2>/dev/null | grep -qi basecamp; then
    info "Verification passed: basecamp plugin is installed"
    return 0
  fi

  error "Plugin installation could not be verified"
}

main() {
  echo ""
  echo "bcq Plugin Installer for Claude Code"
  echo "====================================="
  echo ""

  check_prereqs
  install_plugin
  verify_install

  echo ""
  echo "Done! The bcq plugin is now available in Claude Code."
  echo ""
  echo "Available features:"
  echo "  - /basecamp command for Basecamp workflows"
  echo "  - Session hooks for project context"
  echo "  - Basecamp navigator agent"
  echo ""
}

main "$@"
