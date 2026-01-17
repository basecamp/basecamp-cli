#!/usr/bin/env bash
# install.sh - Install bcq CLI
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
#
# Options (via environment):
#   BCQ_INSTALL_DIR   Where to clone bcq (default: ~/.local/share/bcq)
#   BCQ_BIN_DIR       Where to symlink binary (default: ~/.local/bin)

set -euo pipefail

REPO_URL="https://github.com/basecamp/bcq"
INSTALL_DIR="${BCQ_INSTALL_DIR:-$HOME/.local/share/bcq}"
BIN_DIR="${BCQ_BIN_DIR:-$HOME/.local/bin}"

info() { echo "==> $1"; }
error() { echo "ERROR: $1" >&2; exit 1; }

# Check prerequisites
check_prereqs() {
  local missing=()
  command -v git &>/dev/null || missing+=("git")
  command -v curl &>/dev/null || missing+=("curl")
  command -v jq &>/dev/null || missing+=("jq")
  command -v bash &>/dev/null || missing+=("bash")

  if [[ ${#missing[@]} -gt 0 ]]; then
    error "Missing required tools: ${missing[*]}"
  fi

  # Check bash version (need 4.0+ for associative arrays)
  local bash_version
  bash_version=$(bash --version | head -1 | grep -oE '[0-9]+\.[0-9]+' | head -1)
  local major="${bash_version%%.*}"
  if [[ "$major" -lt 4 ]]; then
    error "bash 4.0+ required (found $bash_version)"
  fi
}

install_bcq() {
  info "Installing bcq to $INSTALL_DIR"

  if [[ -d "$INSTALL_DIR" ]]; then
    info "Updating existing installation..."
    (cd "$INSTALL_DIR" && git pull --ff-only)
  else
    info "Cloning bcq..."
    git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
  fi

  # Create bin directory and symlink
  mkdir -p "$BIN_DIR"
  ln -sf "$INSTALL_DIR/bin/bcq" "$BIN_DIR/bcq"
  chmod +x "$INSTALL_DIR/bin/bcq"

  info "Installed bcq to $BIN_DIR/bcq"
}

setup_path() {
  # Check if BIN_DIR is in PATH
  if [[ ":$PATH:" == *":$BIN_DIR:"* ]]; then
    return 0
  fi

  info "Adding $BIN_DIR to PATH"

  local shell_rc=""
  case "$SHELL" in
    */zsh)  shell_rc="$HOME/.zshrc" ;;
    */bash) shell_rc="$HOME/.bashrc" ;;
    *)      shell_rc="$HOME/.profile" ;;
  esac

  local path_line="export PATH=\"$BIN_DIR:\$PATH\""

  if [[ -f "$shell_rc" ]] && grep -qF "$BIN_DIR" "$shell_rc" 2>/dev/null; then
    info "PATH already configured in $shell_rc"
  else
    echo "" >> "$shell_rc"
    echo "# Added by bcq installer" >> "$shell_rc"
    echo "$path_line" >> "$shell_rc"
    info "Added to $shell_rc"
    info "Run: source $shell_rc"
  fi
}

verify_install() {
  # Try with full path first
  if "$BIN_DIR/bcq" --version &>/dev/null; then
    info "Installation verified!"
    "$BIN_DIR/bcq" --version
    return 0
  fi

  error "Installation failed - bcq not working"
}

main() {
  echo ""
  echo "bcq - Basecamp CLI Installer"
  echo "============================"
  echo ""

  check_prereqs
  install_bcq
  setup_path
  verify_install

  echo ""
  echo "Next steps:"
  echo "  1. Reload your shell: source ~/.bashrc (or ~/.zshrc)"
  echo "  2. Authenticate: bcq auth login"
  echo "  3. Test: bcq projects"
  echo ""
  echo "For Claude Code integration:"
  echo "  claude plugins install github:basecamp/bcq"
  echo ""
}

main "$@"
