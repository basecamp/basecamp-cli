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
REPO="basecamp/basecamp-cli"
BIN_DIR="${BASECAMP_BIN_DIR:-}"
CURL_SCHANNEL_FALLBACK_FLAG=""
CURL_LAST_ERROR=""
CURL_FALLBACK_NOTED=0

# Parse semver: returns 0 if $1 >= $2
version_gte() {
  local v1="$1" v2="$2"
  printf '%s\n%s\n' "$v2" "$v1" | sort -V | head -1 | grep -qx "$v2"
}

# NOTE: Keep the installer helper functions below in sync with scripts/install.sh.
# These scripts stay self-contained on purpose so they can run without sourcing extra files.
path_contains_dir() {
  local dir="$1"
  [[ ":$PATH:" == *":$dir:"* ]]
}

default_bin_dir() {
  local platform="$1"

  if path_contains_dir "$HOME/bin"; then
    echo "$HOME/bin"
    return 0
  fi

  if path_contains_dir "$HOME/.local/bin"; then
    echo "$HOME/.local/bin"
    return 0
  fi

  if [[ "$platform" == windows_* ]]; then
    echo "$HOME/bin"
  else
    echo "$HOME/.local/bin"
  fi
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

detect_curl_fallback() {
  local version_output help_output

  version_output=$(curl --version 2>/dev/null || true)
  if [[ "$version_output" != *[Ss]channel* ]]; then
    return 0
  fi

  help_output=$(curl --help all 2>/dev/null || true)
  if [[ "$help_output" == *"--ssl-revoke-best-effort"* ]]; then
    CURL_SCHANNEL_FALLBACK_FLAG="--ssl-revoke-best-effort"
  elif [[ "$help_output" == *"--ssl-no-revoke"* ]]; then
    CURL_SCHANNEL_FALLBACK_FLAG="--ssl-no-revoke"
  fi
}

curl_run() {
  local err_file status err
  err_file=$(mktemp "${TMPDIR:-/tmp}/basecamp-curl.XXXXXX")

  if curl "$@" 2>"$err_file"; then
    rm -f "$err_file"
    CURL_LAST_ERROR=""
    return 0
  else
    status=$?
  fi

  err=$(<"$err_file")
  rm -f "$err_file"

  if [[ $status -ne 0 ]] && [[ -n "$CURL_SCHANNEL_FALLBACK_FLAG" ]] && [[ "$err" == *"CRYPT_E_NO_REVOCATION_CHECK"* ]]; then
    if [[ $CURL_FALLBACK_NOTED -eq 0 ]]; then
      echo "Retrying curl with ${CURL_SCHANNEL_FALLBACK_FLAG} because Windows certificate revocation checks are unavailable..." >&2
      CURL_FALLBACK_NOTED=1
    fi

    err_file=$(mktemp "${TMPDIR:-/tmp}/basecamp-curl.XXXXXX")
    if curl "$CURL_SCHANNEL_FALLBACK_FLAG" "$@" 2>"$err_file"; then
      rm -f "$err_file"
      CURL_LAST_ERROR=""
      return 0
    else
      status=$?
    fi

    err=$(<"$err_file")
    rm -f "$err_file"
  fi

  CURL_LAST_ERROR="$err"
  return "$status"
}

get_latest_version() {
  local url version api_json

  if url=$(curl_run -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest"); then
    version="${url##*/}"
    version="${version#v}"
    if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      echo "$version"
      return 0
    fi
  fi

  if api_json=$(curl_run -fsSL -H 'Accept: application/vnd.github+json' -H 'User-Agent: basecamp-cli-installer' "https://api.github.com/repos/${REPO}/releases/latest"); then
    version="${api_json#*\"tag_name\":\"v}"
    version="${version%%\"*}"
    if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      echo "$version"
      return 0
    fi
  fi

  echo "Could not determine latest version${CURL_LAST_ERROR:+ ($CURL_LAST_ERROR)}" >&2
  return 1
}

install_basecamp() {
  echo "Installing basecamp..."

  local platform version url archive_name ext tmp_dir

  # Get platform
  platform=$(detect_platform) || return 1
  detect_curl_fallback

  if [[ -z "$BIN_DIR" ]]; then
    BIN_DIR=$(default_bin_dir "$platform")
  fi

  version=$(get_latest_version) || return 1

  # Determine archive extension
  if [[ "$platform" == windows_* ]]; then
    ext="zip"
  else
    ext="tar.gz"
  fi

  archive_name="basecamp_${version}_${platform}.${ext}"
  url="https://github.com/${REPO}/releases/download/v${version}/${archive_name}"

  echo "Downloading basecamp v${version} for ${platform}..."

  tmp_dir=$(mktemp -d)
  trap "rm -rf '${tmp_dir}'" EXIT

  if ! curl_run -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    echo "Failed to download from $url${CURL_LAST_ERROR:+ ($CURL_LAST_ERROR)}" >&2
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
    echo "  export PATH=\"$BIN_DIR:\$PATH\""
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
  BASECAMP_BIN_DIR       Binary directory (default: ~/.local/bin, or ~/bin on Windows)
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
