#!/usr/bin/env bash
# Usage: scripts/release.sh VERSION [--dry-run]
#   VERSION: semver without v prefix (e.g. 0.2.0)
#
# Validates, tags, and pushes to trigger the release workflow.

set -euo pipefail

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
RESET='\033[0m'

info()  { echo -e "${GREEN}==>${RESET} ${BOLD}$*${RESET}"; }
warn()  { echo -e "${YELLOW}WARNING:${RESET} $*"; }
error() { echo -e "${RED}ERROR:${RESET} $*" >&2; }
die()   { error "$@"; exit 1; }

# --- Args ---
VERSION="${1:-}"
DRY_RUN="${DRY_RUN:-false}"
if [[ "$*" == *"--dry-run"* ]]; then
  DRY_RUN=true
fi

if [[ -z "${VERSION}" ]]; then
  echo "Usage: scripts/release.sh VERSION [--dry-run]"
  echo "  VERSION: semver without v prefix (e.g. 0.2.0)"
  exit 1
fi

# --- Validate version format ---
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
  die "Invalid version format: ${VERSION} (expected semver, no v prefix)"
fi

TAG="v${VERSION}"

if [[ "${DRY_RUN}" == "true" || "${DRY_RUN}" == "1" ]]; then
  info "Dry run — no tags will be created or pushed"
  echo ""
fi

# --- Verify branch ---
BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [[ "${BRANCH}" != "main" ]]; then
  die "Must be on main branch (currently on ${BRANCH})"
fi

# --- Verify clean tree ---
if [[ -n "$(git status --porcelain)" ]]; then
  die "Working tree is dirty. Commit or stash changes first."
fi

# --- Verify synced with remote ---
git fetch origin main --quiet
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse origin/main)
if [[ "${LOCAL}" != "${REMOTE}" ]]; then
  die "Local main (${LOCAL:0:7}) is not synced with origin/main (${REMOTE:0:7}). Pull or push first."
fi

# --- Verify no replace directives ---
if grep -q '^[[:space:]]*replace[[:space:]]' go.mod; then
  die "go.mod contains replace directives. Remove them before releasing."
fi

# --- Run pre-flight checks ---
info "Running release checks"
make release-check

# --- Fetch tags to ensure we see remote state ---
git fetch origin --tags --quiet

# --- Handle tag ---
if git rev-parse "${TAG}" >/dev/null 2>&1; then
  EXISTING_SHA=$(git rev-parse "${TAG}^{commit}")
  if [[ "${EXISTING_SHA}" == "${LOCAL}" ]]; then
    info "Tag ${TAG} already exists at HEAD"
  else
    die "Tag ${TAG} already exists at ${EXISTING_SHA:0:7} (not HEAD). Delete it first or choose a different version."
  fi
else
  info "Creating tag ${TAG}"
  if [[ "${DRY_RUN}" == "true" || "${DRY_RUN}" == "1" ]]; then
    echo "  (skipped — dry run)"
  else
    git tag -a "${TAG}" -m "Release ${TAG}"
  fi
fi

# --- Push tag ---
info "Pushing ${TAG} to origin"
if [[ "${DRY_RUN}" == "true" || "${DRY_RUN}" == "1" ]]; then
  echo "  (skipped — dry run)"
else
  git push origin "${TAG}"
fi

# --- Done ---
echo ""
info "Release ${TAG} triggered"
echo ""
echo "  Actions: https://github.com/basecamp/basecamp-cli/actions"
echo "  Release: https://github.com/basecamp/basecamp-cli/releases/tag/${TAG}"
