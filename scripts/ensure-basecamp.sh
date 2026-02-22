#!/usr/bin/env bash
# Verify basecamp is installed and meets minimum version requirements.
# Used by Claude Code skills before executing basecamp commands.
#
# Usage:
#   source ensure-basecamp.sh              # Exits on failure
#   ensure-basecamp.sh --check             # Returns 0/1, prints status
#   ensure-basecamp.sh --install           # Attempts install if missing

set -euo pipefail

MIN_VERSION="${BASECAMP_MIN_VERSION:-0.1.0}"
INSTALL_URL="https://github.com/basecamp/basecamp-cli"
BIN_DIR="${BASECAMP_BIN_DIR:-$HOME/.local/bin}"

# Parse semver: returns 0 if $1 >= $2
version_gte() {
  local v1="$1" v2="$2"
  printf '%s\n%s\n' "$v2" "$v1" | sort -V | head -1 | grep -qx "$v2"
}

check_basecamp() {
  if ! command -v basecamp &>/dev/null; then
    echo "basecamp not found in PATH"
    return 1
  fi

  local version
  version=$(basecamp --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "0.0.0")

  if ! version_gte "$version" "$MIN_VERSION"; then
    echo "basecamp version $version < required $MIN_VERSION"
    return 1
  fi

  echo "basecamp $version OK (>= $MIN_VERSION)"
  return 0
}

detect_platform() {
  local os arch

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin) os="darwin" ;;
    linux) os="linux" ;;
    freebsd) os="freebsd" ;;
    openbsd) os="openbsd" ;;
    mingw*|msys*|cygwin*) os="windows" ;;
    *) echo "Unsupported OS: $os" >&2; return 1 ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) echo "Unsupported architecture: $arch" >&2; return 1 ;;
  esac

  echo "${os}_${arch}"
}

install_basecamp() {
  echo "Installing basecamp..."

  local platform version url archive_name ext tmp_dir

  # Get platform
  platform=$(detect_platform) || return 1

  # Get latest version
  version=$(curl -fsSL "https://api.github.com/repos/basecamp/basecamp-cli/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')
  if [[ -z "$version" ]]; then
    echo "Could not determine latest version" >&2
    return 1
  fi

  # Determine archive extension
  if [[ "$platform" == windows_* ]]; then
    ext="zip"
  else
    ext="tar.gz"
  fi

  archive_name="basecamp_${version}_${platform}.${ext}"
  url="https://github.com/basecamp/basecamp-cli/releases/download/v${version}/${archive_name}"

  echo "Downloading basecamp v${version} for ${platform}..."

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  if ! curl -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    echo "Failed to download from $url" >&2
    return 1
  fi

  # Extract binary
  cd "$tmp_dir"
  if [[ "$ext" == "zip" ]]; then
    unzip -q "$archive_name"
  else
    tar -xzf "$archive_name"
  fi

  # Install binary
  local binary_name="basecamp"
  if [[ "$platform" == windows_* ]]; then
    binary_name="basecamp.exe"
  fi

  mkdir -p "$BIN_DIR"
  mv "$binary_name" "$BIN_DIR/"
  chmod +x "$BIN_DIR/$binary_name"

  echo "Installed basecamp to $BIN_DIR/$binary_name"

  # Check if in PATH
  if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
    echo ""
    echo "Add to your shell profile:"
    echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
  fi
}

main() {
  case "${1:-}" in
    --check)
      check_basecamp
      ;;
    --install)
      if ! check_basecamp 2>/dev/null; then
        install_basecamp
        check_basecamp
      fi
      ;;
    --help|-h)
      cat <<EOF
ensure-basecamp.sh - Verify basecamp installation

Usage:
  ensure-basecamp.sh --check     Check if basecamp is installed and meets version requirements
  ensure-basecamp.sh --install   Install or update basecamp if needed

Environment:
  BASECAMP_MIN_VERSION   Minimum required version (default: $MIN_VERSION)
  BASECAMP_BIN_DIR       Binary directory (default: ~/.local/bin)
EOF
      ;;
    *)
      # Default: check and exit on failure
      if ! check_basecamp; then
        echo ""
        echo "Install basecamp: $INSTALL_URL"
        echo "Or run: $0 --install"
        exit 1
      fi
      ;;
  esac
}

main "$@"
