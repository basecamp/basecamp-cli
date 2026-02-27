#!/usr/bin/env bash
# install.sh - Install basecamp CLI
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
#
# Options (via environment):
#   BASECAMP_BIN_DIR  Where to install binary (default: ~/.local/bin)
#   BASECAMP_VERSION  Specific version to install (default: latest)

set -euo pipefail

REPO="basecamp/basecamp-cli"
BIN_DIR="${BASECAMP_BIN_DIR:-$HOME/.local/bin}"
VERSION="${BASECAMP_VERSION:-}"

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

verify_checksums() {
  local version="$1"
  local tmp_dir="$2"
  local archive_name="$3"
  local base_url="https://github.com/${REPO}/releases/download/v${version}"

  info "Verifying checksums..."

  if ! curl -fsSL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt"; then
    error "Failed to download checksums.txt"
  fi

  # Verify SHA256 checksum of the downloaded archive
  (cd "$tmp_dir" && grep "$archive_name" checksums.txt | shasum -a 256 --check --status) \
    || error "Checksum verification failed for $archive_name"

  info "Checksum verified"

  # If cosign is available, verify the signature
  if command -v cosign &>/dev/null; then
    info "Verifying cosign signature..."

    if ! curl -fsSL "${base_url}/checksums.txt.sig" -o "${tmp_dir}/checksums.txt.sig"; then
      error "Failed to download checksums.txt.sig"
    fi

    if ! curl -fsSL "${base_url}/checksums.txt.pem" -o "${tmp_dir}/checksums.txt.pem"; then
      error "Failed to download checksums.txt.pem"
    fi

    cosign verify-blob \
      --certificate "${tmp_dir}/checksums.txt.pem" \
      --signature "${tmp_dir}/checksums.txt.sig" \
      --certificate-identity-regexp "https://github.com/basecamp/basecamp-cli" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "${tmp_dir}/checksums.txt" \
      || error "Cosign signature verification failed"

    info "Signature verified"
  fi
}

download_binary() {
  local version="$1"
  local platform="$2"
  local tmp_dir="$3"
  local url archive_name ext

  # Determine archive extension
  if [[ "$platform" == windows_* ]]; then
    ext="zip"
  else
    ext="tar.gz"
  fi

  archive_name="basecamp_${version}_${platform}.${ext}"
  url="https://github.com/${REPO}/releases/download/v${version}/${archive_name}"

  info "Downloading basecamp v${version} for ${platform}..."

  if ! curl -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    error "Failed to download from $url"
  fi

  # Verify integrity before extraction
  verify_checksums "$version" "$tmp_dir" "$archive_name"

  # Extract binary
  info "Extracting..."
  cd "$tmp_dir"
  if [[ "$ext" == "zip" ]]; then
    unzip -q "$archive_name"
  else
    tar -xzf "$archive_name"
  fi

  # Find and install binary
  local binary_name="basecamp"
  if [[ "$platform" == windows_* ]]; then
    binary_name="basecamp.exe"
  fi

  if [[ ! -f "$binary_name" ]]; then
    error "Binary not found in archive"
  fi

  mkdir -p "$BIN_DIR"
  mv "$binary_name" "$BIN_DIR/"
  chmod +x "$BIN_DIR/$binary_name"

  info "Installed basecamp to $BIN_DIR/$binary_name"
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
    echo "# Added by basecamp installer" >> "$shell_rc"
    echo "$path_line" >> "$shell_rc"
    info "Added to $shell_rc"
    info "Run: source $shell_rc"
  fi
}

verify_install() {
  if "$BIN_DIR/basecamp" --version &>/dev/null; then
    info "Installation verified!"
    "$BIN_DIR/basecamp" --version
    return 0
  fi

  error "Installation failed - basecamp not working"
}

setup_theme() {
  local basecamp_theme_dir="$HOME/.config/basecamp/theme"
  local omarchy_theme_dir="$HOME/.config/omarchy/current/theme"

  # Skip if basecamp theme already configured
  if [[ -e "$basecamp_theme_dir" ]]; then
    return 0
  fi

  # Link to Omarchy theme if available
  if [[ -d "$omarchy_theme_dir" ]]; then
    info "Linking basecamp theme to system theme"
    mkdir -p "$HOME/.config/basecamp"
    ln -s "$omarchy_theme_dir" "$basecamp_theme_dir" || info "Note: Could not link theme (continuing anyway)"
  fi
}

main() {
  echo ""
  echo "Basecamp CLI - Installer"
  echo "========================"
  echo ""

  # Check for curl
  if ! command -v curl &>/dev/null; then
    error "curl is required but not installed"
  fi

  local platform version tmp_dir
  platform=$(detect_platform)

  if [[ -n "$VERSION" ]]; then
    version="$VERSION"
  else
    version=$(get_latest_version)
  fi

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  download_binary "$version" "$platform" "$tmp_dir"
  setup_path
  setup_theme
  verify_install

  echo ""
  echo "Next steps:"
  echo "  1. Reload your shell: source ~/.bashrc (or ~/.zshrc)"
  echo "  2. Authenticate: basecamp auth login"
  echo "  3. Test: basecamp projects"
  echo ""
}

main "$@"
