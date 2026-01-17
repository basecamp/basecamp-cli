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

# Parse semver: returns 0 if $1 >= $2
version_gte() {
  local v1="$1" v2="$2"
  # Normalize to comparable format
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

install_bcq() {
  echo "Installing bcq..."

  local install_dir="${BCQ_INSTALL_DIR:-$HOME/.local/share/bcq}"
  local bin_dir="${BCQ_BIN_DIR:-$HOME/.local/bin}"

  if [[ -d "$install_dir" ]]; then
    echo "Updating existing installation..."
    (cd "$install_dir" && git pull --ff-only)
  else
    echo "Cloning bcq to $install_dir..."
    git clone --depth 1 "$INSTALL_URL" "$install_dir"
  fi

  # Symlink bcq to bin dir
  mkdir -p "$bin_dir"
  ln -sf "$install_dir/scripts/bcq" "$bin_dir/bcq"

  echo "Installed bcq to $bin_dir/bcq"
  echo "Ensure $bin_dir is in your PATH"

  # Verify
  if [[ ":$PATH:" != *":$bin_dir:"* ]]; then
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
  BCQ_INSTALL_DIR   Installation directory (default: ~/.local/share/bcq)
  BCQ_BIN_DIR       Binary symlink directory (default: ~/.local/bin)
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
