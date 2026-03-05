#!/usr/bin/env bash
# Updates nix/package.nix version and recomputes vendorHash when go.mod changes.
# Usage: scripts/update-nix-flake.sh VERSION
#
# Exit codes:
#   0 — changes made
#   2 — no changes needed

set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: scripts/update-nix-flake.sh VERSION"
  exit 1
fi

NIX_PKG="nix/package.nix"
CHANGED=false

# --- Update version ---
CURRENT_VERSION=$(sed -n 's/.*version = "\([^"]*\)".*/\1/p' "$NIX_PKG" | head -1)
if [[ "$CURRENT_VERSION" != "$VERSION" ]]; then
  sed -i.bak "s/version = \"${CURRENT_VERSION}\"/version = \"${VERSION}\"/" "$NIX_PKG"
  rm -f "${NIX_PKG}.bak"
  CHANGED=true
  echo "  nix version: ${CURRENT_VERSION} → ${VERSION}"
fi

# --- Check if vendorHash needs recomputing ---
PREV_TAG=$(git describe --tags --abbrev=0 HEAD 2>/dev/null || echo "")
NEED_HASH=false
if [[ -z "$PREV_TAG" ]]; then
  NEED_HASH=true
elif ! git diff --quiet "$PREV_TAG"..HEAD -- go.mod go.sum 2>/dev/null; then
  NEED_HASH=true
fi

if [[ "$NEED_HASH" == "true" ]]; then
  if ! command -v docker &>/dev/null; then
    echo "  WARNING: Docker unavailable — cannot recompute vendorHash"
    echo "  Run 'make update-nix-hash' after installing Docker"
  else
    echo "  go.mod changed — computing vendorHash via Docker..."
    BUILD_OUTPUT=$(docker run --rm -v "$(pwd):/src:ro" nixos/nix bash -c '
      cp -a /src /build && cd /build
      rm -rf .git
      git init -q && git add -A && \
        GIT_COMMITTER_NAME=ci GIT_COMMITTER_EMAIL=ci@ci \
        GIT_AUTHOR_NAME=ci GIT_AUTHOR_EMAIL=ci@ci \
        git commit -q -m init
      nix --extra-experimental-features "nix-command flakes" build --no-link 2>&1 || true
    ' 2>&1)

    NEW_HASH=$(echo "$BUILD_OUTPUT" | grep "got:" | awk '{print $2}')

    if [[ -n "$NEW_HASH" ]]; then
      CURRENT_HASH=$(sed -n 's/.*vendorHash = "\([^"]*\)".*/\1/p' "$NIX_PKG" | head -1)
      if [[ "$CURRENT_HASH" != "$NEW_HASH" ]]; then
        sed -i.bak "s|vendorHash = \"${CURRENT_HASH}\"|vendorHash = \"${NEW_HASH}\"|" "$NIX_PKG"
        rm -f "${NIX_PKG}.bak"
        CHANGED=true
        echo "  vendorHash: updated"
      else
        echo "  vendorHash: unchanged"
      fi
    elif echo "$BUILD_OUTPUT" | grep -q "building.*basecamp.*0\." ; then
      echo "  vendorHash: verified (build succeeded)"
    else
      echo "  WARNING: Could not determine vendorHash — check Docker output"
    fi
  fi
else
  echo "  vendorHash: go.mod unchanged, skipping"
fi

if [[ "$CHANGED" == "true" ]]; then
  exit 0
else
  exit 2
fi
