#!/usr/bin/env bash
# Verify bcq is installed and meets minimum version requirements.
# Used by Claude Code skills before executing bcq commands.
#
# Usage:
#   source ensure-bcq.sh              # Exits on failure
#   ensure-bcq.sh --check             # Returns 0/1, prints status
#   ensure-bcq.sh --install           # Attempts install if missing

set -euo pipefail

MIN_VERSION="${BCQ_MIN_VERSION:-0.1.0}"
INSTALL_URL="https://github.com/basecamp/bcq"
BIN_DIR="${BCQ_BIN_DIR:-$HOME/.local/bin}"

# Parse semver: returns 0 if $1 >= $2
version_gte() {
  local v1="$1" v2="$2"
  printf '%s\n%s\n' "$v2" "$v1" | sort -V | head -1 | grep -qx "$v2"
}

check_bcq() {
  if ! command -v bcq &>/dev/null; then
    echo "bcq not found in PATH"
    return 1
  fi

  local version
  version=$(bcq --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "0.0.0")

  if ! version_gte "$version" "$MIN_VERSION"; then
    echo "bcq version $version < required $MIN_VERSION"
    return 1
  fi

  echo "bcq $version OK (>= $MIN_VERSION)"
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

install_bcq() {
  echo "Installing bcq..."

  local platform version url archive_name ext tmp_dir

  # Get platform
  platform=$(detect_platform) || return 1

  # Get latest version
  version=$(curl -fsSL "https://api.github.com/repos/basecamp/bcq/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')
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

  archive_name="bcq_${version}_${platform}.${ext}"
  url="https://github.com/basecamp/bcq/releases/download/v${version}/${archive_name}"

  echo "Downloading bcq v${version} for ${platform}..."

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
  local binary_name="bcq"
  if [[ "$platform" == windows_* ]]; then
    binary_name="bcq.exe"
  fi

  mkdir -p "$BIN_DIR"
  mv "$binary_name" "$BIN_DIR/"
  chmod +x "$BIN_DIR/$binary_name"

  echo "Installed bcq to $BIN_DIR/$binary_name"

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
      check_bcq
      ;;
    --install)
      if ! check_bcq 2>/dev/null; then
        install_bcq
        check_bcq
      fi
      ;;
    --help|-h)
      cat <<EOF
ensure-bcq.sh - Verify bcq installation

Usage:
  ensure-bcq.sh --check     Check if bcq is installed and meets version requirements
  ensure-bcq.sh --install   Install or update bcq if needed

Environment:
  BCQ_MIN_VERSION   Minimum required version (default: $MIN_VERSION)
  BCQ_BIN_DIR       Binary directory (default: ~/.local/bin)
EOF
      ;;
    *)
      # Default: check and exit on failure
      if ! check_bcq; then
        echo ""
        echo "Install bcq: $INSTALL_URL"
        echo "Or run: $0 --install"
        exit 1
      fi
      ;;
  esac
}

main "$@"
