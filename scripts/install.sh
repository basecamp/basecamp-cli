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
  step "Verifying checksums..."

  if ! curl -fsSL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt"; then
    error "Failed to download checksums.txt"
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

    if ! curl -fsSL "${base_url}/checksums.txt.bundle" -o "${tmp_dir}/checksums.txt.bundle"; then
      error "Failed to download checksums.txt.bundle"
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

  if ! curl -fsSL "$url" -o "${tmp_dir}/${archive_name}"; then
    error "Failed to download from $url"
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

  if [[ -n "$VERSION" ]]; then
    version="$VERSION"
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
  "$BIN_DIR/$binary_name" setup
}

main "$@"
