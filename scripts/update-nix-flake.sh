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
# Prerelease tags skip Nix metadata updates, so compare dependency changes
# against the latest stable tag instead of an intervening RC tag.
PREV_TAG=$(git tag --merged HEAD --sort=-version:refname --list 'v[0-9]*.[0-9]*.[0-9]*' | awk '!/-/ { print; exit }')
NEED_HASH=false
if [[ -z "$PREV_TAG" ]]; then
  NEED_HASH=true
elif ! git diff --quiet "$PREV_TAG"..HEAD -- go.mod go.sum 2>/dev/null; then
  NEED_HASH=true
fi

if [[ "$NEED_HASH" == "true" ]]; then
  if ! command -v docker &>/dev/null; then
    echo "ERROR: Docker unavailable — cannot recompute vendorHash"
    echo "Install Docker or run 'make update-nix-hash' manually."
    exit 1
  else
    echo "  go.mod changed — computing vendorHash via Docker..."
    # Pin image digest for supply-chain integrity. Update periodically:
    #   docker pull nixos/nix && docker inspect nixos/nix:latest --format '{{index .RepoDigests 0}}'
    NIX_IMAGE="nixos/nix@sha256:b9c9611c8530fa8049a1215b20638536e1e71dcaf85212e47845112caf3adeea"

    # Runs `nix build` and echoes a trailing NIX_BUILD_EXIT= line so the real
    # exit status survives command substitution. Nothing here may swallow a
    # failure: a `|| true` plus a "did it print 'building basecamp'" heuristic
    # is what let v0.8.0 ship a flake that could not build at all — the log
    # says `building '...basecamp-0.8.0-go-modules.drv'` while *starting* the
    # build it then fails, so the heuristic reported success on a hard failure.
    run_nix_build() {
      docker run --rm -v "$(pwd):/src:ro" "$NIX_IMAGE" bash -c '
        cp -a /src /build && cd /build
        rm -rf .git
        git config --global --add safe.directory /build
        git init -q && git add -A && \
          GIT_COMMITTER_NAME=ci GIT_COMMITTER_EMAIL=ci@ci \
          GIT_AUTHOR_NAME=ci GIT_AUTHOR_EMAIL=ci@ci \
          git commit -q -m init
        nix --extra-experimental-features "nix-command flakes" build --no-link 2>&1
        echo "NIX_BUILD_EXIT=$?"
      ' 2>&1
    }

    nix_build_failed() {
      [[ "$(echo "$1" | grep -c 'NIX_BUILD_EXIT=0')" -eq 0 ]]
    }

    BUILD_OUTPUT=$(run_nix_build)

    if nix_build_failed "$BUILD_OUTPUT"; then
      NEW_HASH=$(echo "$BUILD_OUTPUT" | grep "got:" | awk '{print $2}' || true)
      if [[ -z "$NEW_HASH" ]]; then
        # No hash mismatch, so the build broke for some other reason — a stale
        # flake.lock whose Go is older than go.mod's directive is the one that
        # has actually bitten us. Never continue from here.
        echo "ERROR: nix build failed, and not because of the vendorHash"
        echo "$BUILD_OUTPUT" | tail -25
        exit 1
      fi

      CURRENT_HASH=$(sed -n 's/.*vendorHash = "\([^"]*\)".*/\1/p' "$NIX_PKG" | head -1)
      sed -i.bak "s|vendorHash = \"${CURRENT_HASH}\"|vendorHash = \"${NEW_HASH}\"|" "$NIX_PKG"
      rm -f "${NIX_PKG}.bak"
      CHANGED=true
      echo "  vendorHash: updated"

      # Prove the corrected hash actually builds. The old code trusted the
      # hash it had just written without ever rebuilding.
      echo "  verifying the updated vendorHash builds..."
      BUILD_OUTPUT=$(run_nix_build)
      if nix_build_failed "$BUILD_OUTPUT"; then
        echo "ERROR: nix build still fails after updating the vendorHash"
        echo "$BUILD_OUTPUT" | tail -25
        exit 1
      fi
      echo "  vendorHash: verified (build succeeded)"
    else
      echo "  vendorHash: verified (build succeeded)"
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
