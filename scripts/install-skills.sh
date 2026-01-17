#!/usr/bin/env bash
# install-skills.sh - Install bcq skills for any agent
#
# Usage:
#   ./scripts/install-skills.sh                    # Install to default location
#   ./scripts/install-skills.sh --dir ~/my-skills  # Install to custom directory
#   ./scripts/install-skills.sh --update           # Update existing installation
#
# Environment:
#   BCQ_DIR   Target directory (default: ~/.local/share/bcq)

set -euo pipefail

REPO_URL="https://github.com/basecamp/bcq"
DEFAULT_DIR="${BCQ_DIR:-$HOME/.local/share/bcq}"

info() { echo "==> $1"; }
error() { echo "ERROR: $1" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: install-skills.sh [OPTIONS]

Install bcq skills for any AI agent.

Options:
  --dir DIR     Install to DIR (default: ~/.local/share/bcq)
  --update      Update existing installation
  --help        Show this help

Environment:
  BCQ_DIR   Default target directory

Examples:
  # Install to default location
  ./install-skills.sh

  # Install to custom directory
  ./install-skills.sh --dir ~/agent-skills/bcq

  # Update existing installation
  ./install-skills.sh --update
EOF
}

install_dir=""
update_mode=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)
      install_dir="$2"
      shift 2
      ;;
    --update)
      update_mode=true
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      error "Unknown option: $1"
      ;;
  esac
done

install_dir="${install_dir:-$DEFAULT_DIR}"

install_skills() {
  info "Installing bcq skills to $install_dir"

  if [[ -d "$install_dir/.git" ]]; then
    if [[ "$update_mode" == "true" ]]; then
      info "Updating existing installation..."
      (cd "$install_dir" && git pull --ff-only)
    else
      info "Directory exists. Use --update to update, or remove and reinstall."
      exit 1
    fi
  else
    if [[ -d "$install_dir" ]] && [[ "$(ls -A "$install_dir" 2>/dev/null)" ]]; then
      error "Directory $install_dir exists and is not empty. Remove it first or use a different --dir."
    fi

    info "Cloning bcq repository..."
    git clone --depth 1 "$REPO_URL" "$install_dir"
  fi

  info "Skills installed to: $install_dir/skills/"
  echo ""
  echo "Available skills:"
  for skill in "$install_dir"/skills/*/SKILL.md; do
    if [[ -f "$skill" ]]; then
      name=$(basename "$(dirname "$skill")")
      echo "  - $name: $skill"
    fi
  done
}

print_usage_instructions() {
  echo ""
  echo "To use these skills with your agent:"
  echo ""
  echo "1. Point your agent at the skill files:"
  echo "   $install_dir/skills/<skill-name>/SKILL.md"
  echo ""
  echo "2. Ensure bcq CLI is installed and authenticated:"
  echo "   curl -fsSL https://raw.githubusercontent.com/basecamp/bcq/main/scripts/install.sh | bash"
  echo "   bcq auth login"
  echo ""
  echo "3. To update skills:"
  echo "   $0 --update --dir $install_dir"
  echo "   # or: cd $install_dir && git pull"
  echo ""
}

main() {
  echo ""
  echo "bcq Skills Installer"
  echo "===================="
  echo ""

  install_skills
  print_usage_instructions
}

main
