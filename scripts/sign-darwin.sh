#!/usr/bin/env bash
# Build hook: sign macOS binaries with anchore/quill CLI.
# Called by GoReleaser after each build.
#
# Two modes:
#   CI (QUILL_SIGN_P12 is set): signing is mandatory, missing deps = hard fail
#   Local dev (QUILL_SIGN_P12 unset): silently skip
#
# Inputs (env):
#   QUILL_SIGN_P12       - path to .p12 certificate file (set = CI mode)
#   QUILL_SIGN_PASSWORD  - .p12 unlock password (quill reads this natively)
# Args:
#   $1 - target OS (e.g. "darwin", "linux")
#   $2 - path to built binary
set -euo pipefail

os="$1"
path="$2"

# Non-darwin targets: always skip
[ "$os" = "darwin" ] || exit 0

# No cert path configured: local dev, skip silently
[ -n "${QUILL_SIGN_P12:-}" ] || exit 0

# From here, CI has opted in to signing. Missing deps are errors.
if [ ! -f "$QUILL_SIGN_P12" ]; then
  echo "ERROR: QUILL_SIGN_P12 set but file not found: $QUILL_SIGN_P12" >&2
  exit 1
fi

if [ -z "${QUILL_SIGN_PASSWORD:-}" ]; then
  echo "ERROR: QUILL_SIGN_PASSWORD is not set" >&2
  exit 1
fi

if ! command -v quill >/dev/null; then
  echo "ERROR: quill not found in PATH" >&2
  exit 1
fi

quill sign "$path" --p12 "$QUILL_SIGN_P12"
