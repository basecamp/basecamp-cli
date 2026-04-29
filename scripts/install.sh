#!/usr/bin/env bash
# install.sh - Install basecamp CLI
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/basecamp/basecamp-cli/main/scripts/install.sh | bash
#
# Options (via environment):
#   BASECAMP_BIN_DIR    Where to install binary
#                       (default: ~/bin if on PATH, else ~/.local/bin if on PATH;
#                        otherwise ~/bin on Windows, ~/.local/bin elsewhere)
#   BASECAMP_VERSION    Specific version to install (default: latest)
#   BASECAMP_SKIP_SETUP Set to 1 to skip the interactive setup wizard after install

set -euo pipefail

REPO="basecamp/basecamp-cli"
BIN_DIR="${BASECAMP_BIN_DIR:-}"
VERSION="${BASECAMP_VERSION:-}"
CURL_SCHANNEL_FALLBACK_FLAG=""
CURL_LAST_ERROR=""
CURL_FALLBACK_NOTED=0

# Color helpers — respect NO_COLOR (https://no-color.org)
if [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; then
  bold()  { printf '\033[1m%s\033[0m' "$1"; }
  green() { printf '\033[32m%s\033[0m' "$1"; }
  red()   { printf '\033[31m%s\033[0m' "$1"; }
  dim()   { printf '\033[2m%s\033[0m' "$1"; }
else
  bold()  { printf '%s' "$1"; }
  green() { printf '%s' "$1"; }
  red()   { printf '%s' "$1"; }
  dim()   { printf '%s' "$1"; }
fi

info()  { echo "  $(green "✓") $1"; }
step()  { echo "  $(bold "→") $1"; }
error() { echo "  $(red "✗ ERROR:") $1" >&2; exit 1; }

find_sha256_cmd() {
  if command -v sha256sum &>/dev/null; then
    echo "sha256sum"
  elif command -v shasum &>/dev/null; then
    echo "shasum -a 256"
  else
    error "No SHA256 tool found (need sha256sum or shasum)"
  fi
}

# NOTE: Keep the installer helper functions below in sync with scripts/ensure-basecamp.sh.
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
      echo "  $(bold "→") Windows certificate revocation checks are unavailable; retrying curl with ${CURL_SCHANNEL_FALLBACK_FLAG}" >&2
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

  # Follow the releases/latest redirect to get the version from the final URL.
  # Avoids the GitHub API (no rate limiting) and grep/sed (better Windows compat).
  if url=$(curl_run -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest"); then
    version="${url##*/}"
    version="${version#v}"
    if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      echo "$version"
      return 0
    fi
  fi

  # Fallback to the GitHub API if redirect parsing fails. Whitespace-tolerant
  # regex so a future GitHub format change (pretty-print, extra spaces) doesn't
  # silently break the fallback. Pure bash so no GNU-awk dependency.
  if api_json=$(curl_run -fsSL -H 'Accept: application/vnd.github+json' -H 'User-Agent: basecamp-cli-installer' "https://api.github.com/repos/${REPO}/releases/latest"); then
    if [[ $api_json =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"v?([^\"]+)\" ]]; then
      version="${BASH_REMATCH[1]}"
      if [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        echo "$version"
        return 0
      fi
    fi
  fi

  error "Could not determine latest version. ${CURL_LAST_ERROR:+curl said: ${CURL_LAST_ERROR}. }If native Windows curl fails, try Scoop or PowerShell. If using Git Bash, try /usr/bin/curl instead."
}

verify_checksums() {
  local version="$1"
  local tmp_dir="$2"
  local archive_name="$3"
  local base_url="https://github.com/${REPO}/releases/download/v${version}"
  step "Verifying checksums..."

  if ! curl_run -fsSL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt"; then
    error "Failed to download checksums.txt${CURL_LAST_ERROR:+ (${CURL_LAST_ERROR})}"
  fi

  # Verify SHA256 checksum of the downloaded archive
  local expected actual
  expected=$(awk -v f="$archive_name" '$2 == f || $2 == ("*" f) {print $1; exit}' "${tmp_dir}/checksums.txt")
  actual=$(cd "$tmp_dir" && $(find_sha256_cmd) "$archive_name" | awk '{print $1}')
  [[ -n "$expected" && "$expected" == "$actual" ]]  \
    || error "Checksum verification failed for $archive_name"

  info "Checksum verified"

  # If cosign is available, verify the signature
  if command -v cosign &>/dev/null; then
    step "Verifying cosign signature..."

    if ! curl_run -fsSL "${base_url}/checksums.txt.bundle" -o "${tmp_dir}/checksums.txt.bundle"; then
      error "Failed to download checksums.txt.bundle${CURL_LAST_ERROR:+ (${CURL_LAST_ERROR})}"
    fi

    cosign verify-blob \
      --bundle "${tmp_dir}/checksums.txt.bundle" \
      --certificate-identity "https://github.com/basecamp/basecamp-cli/.github/workflows/release.yml@refs/tags/v${version}" \
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

  step "Downloading basecamp v${version} for ${platform}..."

  if ! curl_run -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    error "Failed to download from $url${CURL_LAST_ERROR:+ (${CURL_LAST_ERROR})}"
  fi

  # Verify integrity before extraction
  verify_checksums "$version" "$tmp_dir" "$archive_name"

  # Extract binary
  step "Extracting..."
  if [[ "$ext" == "zip" ]]; then
    unzip -q "${tmp_dir}/${archive_name}" -d "$tmp_dir"
  else
    tar -xzf "${tmp_dir}/${archive_name}" -C "$tmp_dir"
  fi

  # Find and install binary
  local binary_name="basecamp"
  if [[ "$platform" == windows_* ]]; then
    binary_name="basecamp.exe"
  fi

  if [[ ! -f "${tmp_dir}/${binary_name}" ]]; then
    error "Binary not found in archive"
  fi

  mkdir -p "$BIN_DIR"
  mv "${tmp_dir}/${binary_name}" "$BIN_DIR/"
  chmod +x "$BIN_DIR/$binary_name"

  info "Installed basecamp to $BIN_DIR/$binary_name"
}

setup_path() {
  # Check if BIN_DIR is in PATH
  if [[ ":$PATH:" == *":$BIN_DIR:"* ]]; then
    return 0
  fi

  step "Adding $BIN_DIR to PATH"

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
  local platform="$1"
  local binary_name="basecamp"
  if [[ "$platform" == windows_* ]]; then
    binary_name="basecamp.exe"
  fi

  local installed_version
  if installed_version=$("$BIN_DIR/$binary_name" --version 2>/dev/null); then
    info "$(green "${installed_version} installed")"
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
    step "Linking basecamp theme to system theme"
    mkdir -p "$HOME/.config/basecamp"
    ln -s "$omarchy_theme_dir" "$basecamp_theme_dir" || info "Note: Could not link theme (continuing anyway)"
  fi
}

show_banner() {
  # Skip braille art if terminal is too narrow (logo 32 + gap 3 + text 8 = 43)
  local cols
  cols=$(tput cols 2>/dev/null || echo 80)
  if [[ "$cols" -ge 44 ]]; then
    local y="" b="" r=""
    if [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; then
      y=$'\033[38;2;232;162;23m'  # brand yellow #e8a217
      b=$'\033[1m'                # bold
      r=$'\033[0m'                # reset
    fi

    local logo=(
      "⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣠⣤⣶⣶⣶⣶⣶⣶⣦⣤⣀"
      "⠀⠀⠀⠀⠀⠀⠀⢀⣴⣾⣿⣿⣿⠿⠿⠛⠛⠛⠻⠿⣿⣿⣿⣦⣀"
      "⠀⠀⠀⠀⠀⢀⣴⣿⣿⡿⠛⠁⠀⠀⠀⠀⠀⠀⠀⠀⠀⠙⠻⣿⣿⣦⡀"
      "⠀⠀⠀⠀⣴⣿⣿⡿⠋⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠘⢿⣿⣿⣄"
      "⠀⠀⢀⣼⣿⣿⠏⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣠⣤⡀⠀⠀⠀⠀⢻⣿⣿⣆"
      "⠀⢀⣾⣿⣿⠃⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣼⣿⣿⠃⠀⠀⠀⠀⠀⢻⣿⣿⡄"
      "⠀⣼⣿⣿⠃⠀⠀⣠⣶⣿⣷⣦⣄⠀⠀⢀⣼⣿⣿⠃⠀⠀⠀⠀⠀⠀⠀⢿⣿⣿⡀"
      "⢸⣿⣿⠇⠀⢠⣾⣿⡿⠛⠻⣿⣿⣷⣤⣾⣿⡿⠃⠀⠀⠀⠀⠀⠀⠀⠀⠘⣿⣿⣇"
      "⠈⠉⠉⠀⢠⣿⣿⡟⠁⠀⠀⠈⠻⣿⣿⣿⠟⠁⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢻⣿⣿"
      "⠀⠀⠀⢠⣿⣿⡟⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⡇"
      "⠀⠀⠀⢻⣿⣿⣦⡀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣼⣿⣿⠇"
      "⠀⠀⠀⠀⠙⠿⣿⣿⣷⣤⣀⡀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣀⣤⣶⣿⣿⡿⠋"
      "⠀⠀⠀⠀⠀⠀⠈⠛⠿⣿⣿⣿⣿⣶⣶⣶⣶⣶⣶⣶⣶⣶⣿⣿⣿⣿⡿⠟⠉"
      "⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠉⠙⠛⠛⠿⠿⠿⠿⠿⠿⠟⠛⠛⠉⠉"
    )

    local text_line=6

    echo ""
    if [[ -t 1 ]] && [[ -z "${NO_COLOR:-}" ]]; then
      # Animated reveal on TTY (skip cursor movement when NO_COLOR is set)
      for line in "${logo[@]}"; do
        echo "${y}${line}${r}"
        sleep 0.03
      done

      # Type "Basecamp" to the right of the logo via cursor repositioning
      sleep 0.1
      local text="Basecamp"
      local lines_up=$(( ${#logo[@]} - text_line ))
      printf "\033[${lines_up}A\033[36G"
      for (( i=0; i<${#text}; i++ )); do
        printf "${b}${text:$i:1}${r}"
        sleep 0.03
      done
      printf "\033[${lines_up}B\r"
    else
      # Static output when piped — no sleeps, no cursor movement
      for i in "${!logo[@]}"; do
        if [[ "$i" -eq "$text_line" ]]; then
          echo "${logo[$i]}   Basecamp"
        else
          echo "${logo[$i]}"
        fi
      done
    fi
    echo ""
  else
    echo ""
    echo "Basecamp CLI"
    echo ""
  fi
}

main() {
  show_banner

  # Check for curl
  if ! command -v curl &>/dev/null; then
    error "curl is required but not installed"
  fi

  local platform version tmp_dir
  platform=$(detect_platform)
  detect_curl_fallback

  if [[ -z "$BIN_DIR" ]]; then
    BIN_DIR=$(default_bin_dir "$platform")
  fi

  if [[ -n "$VERSION" ]]; then
    version="$VERSION"
    if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      error "Invalid version '${version}'. Expected semver format (e.g. 1.2.3 or 1.2.3-rc.1)."
    fi
  else
    version=$(get_latest_version)
  fi

  tmp_dir=$(mktemp -d)
  trap "rm -rf '${tmp_dir}'" EXIT

  local binary_name="basecamp"
  if [[ "$platform" == windows_* ]]; then
    binary_name="basecamp.exe"
  fi

  download_binary "$version" "$platform" "$tmp_dir"
  setup_path
  setup_theme
  verify_install "$platform"

  echo ""

  # Run interactive setup wizard only when stdin is a TTY and not explicitly skipped.
  # Non-interactive environments (CI, piped input, coding agents like Claude Code)
  # get the agent skill installed and next-step instructions instead — the wizard
  # requires interactive prompts that don't work without a terminal.
  if [[ "${BASECAMP_SKIP_SETUP:-}" == "1" ]]; then
    step "Skipping setup wizard (BASECAMP_SKIP_SETUP=1)"
    "$BIN_DIR/$binary_name" setup claude || true
    echo ""
    echo "  Next steps:"
    echo "    $(bold "basecamp auth login")        Authenticate with Basecamp"
    echo "    $(bold "basecamp setup")             Run interactive setup wizard"
    echo ""
  elif [[ -t 0 ]] && [[ -t 1 ]]; then
    "$BIN_DIR/$binary_name" setup
  else
    info "Skipping interactive setup (no terminal detected)."
    "$BIN_DIR/$binary_name" setup claude || true
    echo ""
    echo "  Next steps:"
    echo "    $(bold "basecamp auth login")        Authenticate with Basecamp"
    echo "    $(bold "basecamp setup")             Run interactive setup wizard"
    echo ""
  fi
}

main "$@"
