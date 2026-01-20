#!/usr/bin/env bash
# bcq installer
#
# Install:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/install.sh | bash
#
# Update:
#   Run the installer again
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/install.sh | bash -s -- --uninstall

set -euo pipefail

# Colors (if terminal supports them)
if [[ -t 1 ]]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  BLUE='\033[0;34m'
  NC='\033[0m' # No Color
else
  RED='' GREEN='' YELLOW='' BLUE='' NC=''
fi

info() { echo -e "${BLUE}==>${NC} $*"; }
success() { echo -e "${GREEN}==>${NC} $*"; }
warn() { echo -e "${YELLOW}Warning:${NC} $*"; }
error() { echo -e "${RED}Error:${NC} $*" >&2; }

# Configuration
BCQ_VERSION="${BCQ_VERSION:-main}"
INSTALL_DIR="${BCQ_INSTALL_DIR:-$HOME/.local/share/bcq}"
BIN_DIR="${BCQ_BIN_DIR:-$HOME/.local/bin}"
REPO_URL="https://github.com/basecamp/bcq"

# Parse arguments
UNINSTALL=false
for arg in "$@"; do
  case "$arg" in
    --uninstall) UNINSTALL=true ;;
  esac
done

# Check dependencies
check_dependencies() {
  local missing=()

  if ! command -v curl &>/dev/null && ! command -v wget &>/dev/null; then
    missing+=("curl or wget")
  fi

  if ! command -v jq &>/dev/null; then
    missing+=("jq")
  fi

  if ! command -v git &>/dev/null; then
    missing+=("git")
  fi

  # Check bash version
  local bash_version
  bash_version=$(bash --version | head -1 | grep -oE '[0-9]+\.[0-9]+' | head -1)
  local bash_major="${bash_version%%.*}"
  if (( bash_major < 4 )); then
    warn "System bash is version $bash_version (need 4+)"
    if [[ "$(uname)" == "Darwin" ]]; then
      if command -v /opt/homebrew/bin/bash &>/dev/null; then
        info "Homebrew bash found at /opt/homebrew/bin/bash"
      else
        missing+=("bash 4+ (brew install bash)")
      fi
    else
      missing+=("bash 4+")
    fi
  fi

  if [[ ${#missing[@]} -gt 0 ]]; then
    error "Missing dependencies: ${missing[*]}"
    echo ""
    echo "Please install the missing dependencies and try again."
    if [[ "$(uname)" == "Darwin" ]]; then
      echo "  brew install ${missing[*]}"
    elif command -v apt-get &>/dev/null; then
      echo "  sudo apt-get install ${missing[*]}"
    elif command -v dnf &>/dev/null; then
      echo "  sudo dnf install ${missing[*]}"
    fi
    exit 1
  fi
}

# Download or update bcq
install_bcq() {
  info "Installing bcq to $INSTALL_DIR..."

  mkdir -p "$INSTALL_DIR"
  mkdir -p "$BIN_DIR"

  if [[ -d "$INSTALL_DIR/.git" ]]; then
    info "Updating existing installation..."
    cd "$INSTALL_DIR"
    git fetch origin
    git checkout "$BCQ_VERSION"
    if [[ "$BCQ_VERSION" == "main" ]]; then
      git pull origin main
    fi
  else
    info "Cloning bcq repository..."
    git clone --depth 1 --branch "$BCQ_VERSION" "$REPO_URL" "$INSTALL_DIR" 2>/dev/null || \
      git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
  fi

  # Create wrapper script in bin
  info "Creating bcq command in $BIN_DIR..."

  # Find bash 4+
  local bash_cmd="/usr/bin/env bash"
  if [[ "$(uname)" == "Darwin" ]]; then
    if [[ -x /opt/homebrew/bin/bash ]]; then
      bash_cmd="/opt/homebrew/bin/bash"
    elif [[ -x /usr/local/bin/bash ]]; then
      bash_cmd="/usr/local/bin/bash"
    fi
  fi

  cat > "$BIN_DIR/bcq" <<EOF
#!$bash_cmd
export BCQ_ROOT="$INSTALL_DIR"
exec $bash_cmd "\$BCQ_ROOT/bin/bcq" "\$@"
EOF

  chmod +x "$BIN_DIR/bcq"
}

# Setup shell completions
setup_completions() {
  info "Setting up shell completions..."

  # Bash completion
  if [[ -d "$HOME/.bash_completion.d" ]]; then
    ln -sf "$INSTALL_DIR/completions/bcq.bash" "$HOME/.bash_completion.d/bcq"
  elif [[ -d "/etc/bash_completion.d" ]] && [[ -w "/etc/bash_completion.d" ]]; then
    ln -sf "$INSTALL_DIR/completions/bcq.bash" "/etc/bash_completion.d/bcq"
  fi

  # Zsh completion
  if [[ -d "$HOME/.zsh/completions" ]]; then
    ln -sf "$INSTALL_DIR/completions/bcq.zsh" "$HOME/.zsh/completions/_bcq"
  fi
}

# Check if BIN_DIR is in PATH
check_path() {
  if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
    warn "$BIN_DIR is not in your PATH"
    echo ""
    echo "Add it to your shell config:"
    echo ""
    if [[ -n "${ZSH_VERSION:-}" ]] || [[ "$SHELL" == *zsh ]]; then
      echo "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc"
      echo "  source ~/.zshrc"
    else
      echo "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.bashrc"
      echo "  source ~/.bashrc"
    fi
    echo ""
  fi
}

# Print success message
print_success() {
  echo ""
  success "bcq installed successfully!"
  echo ""
  echo "Get started:"
  echo "  bcq auth login      # Authenticate with Basecamp"
  echo "  bcq projects        # List your projects"
  echo "  bcq todos           # Your assigned todos"
  echo "  bcq --help          # Full command reference"
  echo ""
  echo "To update later: run this installer again"
  echo ""
}

# Uninstall
uninstall_bcq() {
  info "Uninstalling bcq..."

  if [[ -f "$BIN_DIR/bcq" ]]; then
    rm -f "$BIN_DIR/bcq"
    info "Removed $BIN_DIR/bcq"
  fi

  if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    info "Removed $INSTALL_DIR"
  fi

  # Clean up completions
  [[ -L "$HOME/.bash_completion.d/bcq" ]] && rm -f "$HOME/.bash_completion.d/bcq"
  [[ -L "$HOME/.zsh/completions/_bcq" ]] && rm -f "$HOME/.zsh/completions/_bcq"

  echo ""
  success "bcq uninstalled"
  echo ""
  echo "Config and credentials preserved at ~/.config/basecamp/"
  echo "To remove completely: rm -rf ~/.config/basecamp ~/.cache/bcq"
  echo ""
}

# Main
main() {
  echo ""
  info "bcq installer"
  echo ""

  if [[ "$UNINSTALL" == "true" ]]; then
    uninstall_bcq
    return
  fi

  check_dependencies
  install_bcq
  setup_completions
  check_path
  print_success
}

main "$@"
