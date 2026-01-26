#!/usr/bin/env bash
# install.sh - Install bcq CLI
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash
#
# Options (via environment):
#   BCQ_BIN_DIR       Where to install binary (default: ~/.local/bin)
#   BCQ_VERSION       Specific version to install (default: latest)

set -euo pipefail

REPO="basecamp/bcq"
BIN_DIR="${BCQ_BIN_DIR:-$HOME/.local/bin}"
VERSION="${BCQ_VERSION:-}"

info() { echo "==> $1"; }
error() { echo "ERROR: $1" >&2; exit 1; }

detect_platform() {
  local os arch

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin) os="darwin" ;;
    linux) os="linux" ;;
    freebsd) os="freebsd" ;;
    openbsd) os="openbsd" ;;
    mingw*|msys*|cygwin*) os="windows" ;;
    *) error "Unsupported OS: $os" ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) error "Unsupported architecture: $arch" ;;
  esac

  echo "${os}_${arch}"
}

get_latest_version() {
  local version
  version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')
  if [[ -z "$version" ]]; then
    error "Could not determine latest version. Check your network connection."
  fi
  echo "$version"
}

download_binary() {
  local version="$1"
  local platform="$2"
  local url archive_name ext

  # Determine archive extension
  if [[ "$platform" == windows_* ]]; then
    ext="zip"
  else
    ext="tar.gz"
  fi

  archive_name="bcq_${version}_${platform}.${ext}"
  url="https://github.com/${REPO}/releases/download/v${version}/${archive_name}"

  info "Downloading bcq v${version} for ${platform}..."

  local tmp_dir
  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  if ! curl -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    error "Failed to download from $url"
  fi

  # Extract binary
  info "Extracting..."
  cd "$tmp_dir"
  if [[ "$ext" == "zip" ]]; then
    unzip -q "$archive_name"
  else
    tar -xzf "$archive_name"
  fi

  # Find and install binary
  local binary_name="bcq"
  if [[ "$platform" == windows_* ]]; then
    binary_name="bcq.exe"
  fi

  if [[ ! -f "$binary_name" ]]; then
    error "Binary not found in archive"
  fi

  mkdir -p "$BIN_DIR"
  mv "$binary_name" "$BIN_DIR/"
  chmod +x "$BIN_DIR/$binary_name"

  info "Installed bcq to $BIN_DIR/$binary_name"
}

setup_path() {
  # Check if BIN_DIR is in PATH
  if [[ ":$PATH:" == *":$BIN_DIR:"* ]]; then
    return 0
  fi

  info "Adding $BIN_DIR to PATH"

  local shell_rc=""
  case "${SHELL:-}" in
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
  if "$BIN_DIR/bcq" --version &>/dev/null; then
    info "Installation verified!"
    "$BIN_DIR/bcq" --version
    return 0
  fi

  error "Installation failed - bcq not working"
}

setup_theme() {
  local bcq_theme_dir="$HOME/.config/bcq/theme"
  local omarchy_theme_dir="$HOME/.config/omarchy/current/theme"

  # Skip if bcq theme already configured
  if [[ -e "$bcq_theme_dir" ]]; then
    return 0
  fi

  # Link to Omarchy theme if available
  if [[ -d "$omarchy_theme_dir" ]]; then
    info "Linking bcq theme to system theme"
    mkdir -p "$HOME/.config/bcq"
    ln -s "$omarchy_theme_dir" "$bcq_theme_dir" || info "Note: Could not link theme (continuing anyway)"
  fi
}

main() {
  echo ""
  echo "bcq (Basecamp Query) - Installer"
  echo "================================="
  echo ""

  # Check for curl
  if ! command -v curl &>/dev/null; then
    error "curl is required but not installed"
  fi

  local platform version
  platform=$(detect_platform)

  if [[ -n "$VERSION" ]]; then
    version="$VERSION"
  else
    version=$(get_latest_version)
  fi

  download_binary "$version" "$platform"
  setup_path
  setup_theme
  verify_install

  echo ""
  echo "Next steps:"
  echo "  1. Reload your shell: source ~/.bashrc (or ~/.zshrc)"
  echo "  2. Authenticate: bcq auth login"
  echo "  3. Test: bcq projects"
  echo ""
}

main "$@"
