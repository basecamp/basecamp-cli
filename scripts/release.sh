#!/usr/bin/env bash
# Usage: scripts/release.sh VERSION [--dry-run]
#   VERSION: semver without v prefix (e.g. 0.2.0)
#
# Validates, then tags and pushes the tag to trigger the release workflow. Set
# DRY_RUN=1 (or pass --dry-run) to run the checks only.
#
# A stable release ships its version in nix/package.nix and both plugin
# manifests, and main takes changes only through pull requests, so a stable
# release is two runs. The first finds the metadata unstamped, stamps it on
# release/vX.Y.Z, opens a PR and stops. Once that PR is merged, the second run
# from main finds the metadata stamped and pushes only the tag. A prerelease
# leaves the metadata alone and tags in one run. The script never pushes to
# main.

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
PRERELEASE=false
if [[ "${VERSION}" == *-* ]]; then
  PRERELEASE=true
fi
is_dry_run() { [[ "${DRY_RUN}" == "true" || "${DRY_RUN}" == "1" ]]; }

if is_dry_run; then
  info "Dry run — no branches, PRs or tags will be created or pushed"
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

# From here on the tree is known clean, so on failure the stable metadata files
# can be checked out to undo half-made edits: a failed Nix update or plugin
# stamp must not leave a dirty tree that blocks the next attempt. Checkout from
# HEAD, not the index, so a failed release commit (hook, signing) is unstaged
# too. A failure while preparing the release PR, which stamps on a detached
# HEAD, also puts the clone back on main, where the next attempt starts.
METADATA_FILES=(nix/package.nix .claude-plugin/plugin.json .codex-plugin/plugin.json)
restore_release_metadata() {
  if [[ "${1:-1}" -ne 0 ]]; then
    git checkout --quiet HEAD -- "${METADATA_FILES[@]}" || true
    if [[ "$(git rev-parse --abbrev-ref HEAD)" != "main" ]]; then
      git switch --quiet main || true
    fi
  fi
}
trap 'restore_release_metadata "$?"' EXIT

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

# --- Verify required tools ---
if ! command -v jq >/dev/null 2>&1; then
  die "jq is required but not found. Install with your package manager."
fi

# --- Stable release metadata ---
# A stable tag must point at a commit whose nix/package.nix and both plugin
# manifests carry its version: the plugin installs from the `stable` branch the
# release workflow moves to the tag.
nix_version()    { sed -n 's/.*version = "\([^"]*\)".*/\1/p' nix/package.nix | head -1; }
claude_version() { jq -r .version .claude-plugin/plugin.json; }
codex_version()  { jq -r .version .codex-plugin/plugin.json; }
metadata_stamped() {
  [[ "$(nix_version)" == "${VERSION}" && "$(claude_version)" == "${VERSION}" && "$(codex_version)" == "${VERSION}" ]]
}

# --- Validate the tag before anything is pushed ---
git fetch origin --tags --quiet
if git rev-parse -q --verify "refs/tags/${TAG}^{commit}" >/dev/null; then
  EXISTING_SHA=$(git rev-parse "refs/tags/${TAG}^{commit}")
  if [[ "${EXISTING_SHA}" != "${LOCAL}" ]]; then
    die "Tag ${TAG} already exists at ${EXISTING_SHA:0:7} (not HEAD). Delete it first or choose a different version."
  fi
  # A tag already at HEAD can only be re-run if HEAD carries the stamps: the
  # release PR would stamp a commit the tag does not point at.
  if [[ "${PRERELEASE}" != "true" ]] && ! metadata_stamped; then
    die "Tag ${TAG} already exists at HEAD, but HEAD's release metadata is not stamped for it (nix $(nix_version), Claude plugin $(claude_version), Codex plugin $(codex_version)). Choose a different version."
  fi
fi

# --- Run pre-flight checks ---
info "Running release checks"
# DRY_RUN is this script's (true/false); the skill-sync tests read a DRY_RUN
# of their own (unset, local or remote) and refuse anything else. Keep it out of
# the checks' environment. `make release DRY_RUN=1` also carries it into the
# nested make through MAKEFLAGS, which unsetting the shell variable does not
# reach; the empty command-line assignment overrides that.
env -u DRY_RUN make DRY_RUN= release-check

next_steps() {
  echo ""
  echo "  1. Review and merge $1"
  echo "  2. From an up-to-date main, run: make release VERSION=${VERSION}"
  echo "     It finds the metadata stamped and pushes only the tag."
}

# The first run of a stable release: stamp the metadata on release/vX.Y.Z, push
# that branch and open the release PR. Never main.
open_release_pr() {
  local branch="release/${TAG}" existing url staged files_md commit_msg nix_rc=0
  command -v gh >/dev/null 2>&1 || die "gh is required to open the release PR (https://cli.github.com)."

  existing=$(gh pr list --head "${branch}" --base main --state open --json url --jq '.[0].url // empty')
  if [[ -n "${existing}" ]]; then
    info "The release PR for ${TAG} is already open: ${existing}"
    next_steps "${existing}"
    return
  fi
  # A branch on origin with no open PR is an earlier attempt. Pushing over it
  # could discard someone's work, so say what is there instead.
  if [[ -n "$(git ls-remote --heads origin "${branch}")" ]]; then
    die "origin already has ${branch} but no open PR from it. Open a PR from it, or delete it (git push origin --delete ${branch}) and re-run."
  fi

  # Stamp on a detached HEAD and push it as the branch: no local branch of
  # that name is created, so none left from an earlier attempt is reset.
  git switch --quiet --detach

  info "Updating Nix flake"
  scripts/update-nix-flake.sh "${VERSION}" || nix_rc=$?
  if [[ "${nix_rc}" -eq 2 ]]; then
    echo "  nix flake: no changes needed"
  elif [[ "${nix_rc}" -ne 0 ]]; then
    die "scripts/update-nix-flake.sh failed (exit ${nix_rc})"
  fi

  info "Stamping plugin version"
  scripts/stamp-plugin-version.sh "${VERSION}"
  scripts/stamp-codex-plugin-version.sh "${VERSION}"

  git add nix/package.nix .claude-plugin/plugin.json .codex-plugin/plugin.json
  staged=$(git diff --cached --name-only)
  if grep -q '^nix/package\.nix$' <<<"${staged}" && grep -qE '^\.(claude|codex)-plugin/plugin\.json$' <<<"${staged}"; then
    commit_msg="Update nix flake and plugin version for ${TAG}"
  elif grep -q '^nix/package\.nix$' <<<"${staged}"; then
    commit_msg="Update nix flake for ${TAG}"
  else
    commit_msg="Update plugin version for ${TAG}"
  fi
  git commit --quiet -m "${commit_msg}"
  git push --quiet origin "HEAD:refs/heads/${branch}"

  # One Markdown list item per file the commit touches, each in backticks.
  # shellcheck disable=SC2016
  files_md=$(sed 's/^/- `/; s/$/`/' <<<"${staged}")

  url=$(gh pr create --base main --head "${branch}" --label release \
    --title "Prepare ${TAG} release" \
    --body "Stamps ${VERSION} for ${TAG} into the release metadata a stable tag must carry:

${files_md}

Opened by \`scripts/release.sh\`: \`main\` takes changes only through pull requests. Once this is merged, running \`make release VERSION=${VERSION}\` again from an up-to-date \`main\` finds the metadata stamped and pushes only the tag.")
  git switch --quiet main
  info "Opened the release PR: ${url}"
  next_steps "${url}"
}

if [[ "${PRERELEASE}" == "true" ]]; then
  info "Skipping stable release metadata for prerelease"
  echo "  nix flake: unchanged"
  echo "  Claude plugin metadata: unchanged"
  echo "  Codex plugin metadata: unchanged"
elif ! metadata_stamped; then
  if is_dry_run; then
    info "Release metadata is not stamped for ${VERSION}"
    echo "  A real run stamps it on release/${TAG}, opens the release PR and stops;"
    echo "  the tag is pushed by the run after that PR is merged."
    echo ""
    info "Dry run complete. No branch, PR or tag created."
    exit 0
  fi
  open_release_pr
  exit 0
else
  info "Release metadata is stamped for ${VERSION}"
  # The stamps say nothing about what merged since the release PR was built: a
  # dependency or toolchain change can break the flake at the commit about to
  # be tagged, and the release workflow verifies Nix only after publishing.
  # Verify it here, at the tagged commit, as the single-run release did.
  info "Verifying the Nix flake at ${LOCAL:0:7}"
  if is_dry_run; then
    echo "  (skipped — dry run)"
  else
    NIX_RC=0
    scripts/update-nix-flake.sh "${VERSION}" || NIX_RC=$?
    if [[ "${NIX_RC}" -eq 0 ]]; then
      die "nix/package.nix needs an update at ${LOCAL:0:7} (above). Nothing was tagged; land it through a PR (make update-nix-hash) and re-run."
    elif [[ "${NIX_RC}" -ne 2 ]]; then
      die "scripts/update-nix-flake.sh failed (exit ${NIX_RC}). Nothing was tagged."
    fi
    echo "  nix flake: verified"
  fi
fi

# --- Handle tag ---
if git rev-parse -q --verify "refs/tags/${TAG}^{commit}" >/dev/null; then
  info "Tag ${TAG} already exists at HEAD"
else
  info "Creating tag ${TAG}"
  if is_dry_run; then
    echo "  (skipped — dry run)"
  else
    git tag -a "${TAG}" -m "Release ${TAG}"
  fi
fi

# --- Push tag ---
# Only the tag is pushed. On a rejection (another operator took the tag, or a
# repository rule refused it) the local tag goes, so the next attempt starts
# clean.
info "Pushing ${TAG} to origin"
if is_dry_run; then
  echo "  (skipped — dry run)"
elif ! git push origin "refs/tags/${TAG}"; then
  git tag -d "${TAG}" >/dev/null 2>&1 || true
  git fetch origin --tags --quiet || true
  die "The push of ${TAG} was rejected (above). Nothing was pushed and the local tag was removed."
fi

# --- Done ---
echo ""
info "Release ${TAG} triggered"
echo ""
echo "  Actions: https://github.com/basecamp/basecamp-cli/actions"
echo "  Release: https://github.com/basecamp/basecamp-cli/releases/tag/${TAG}"
