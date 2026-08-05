#!/usr/bin/env bash
# Verify every workflow lints with the same golangci-lint version.
#
# A stale pin here does not fail a PR — it fails whichever job is the odd one
# out, at the moment that job runs. release.yml sat on v2.9.0 while test.yml and
# security.yml moved to v2.11.1, and the drift surfaced as a gosec G115 false
# positive that blocked a release tag after every PR check had gone green. The
# "keep in lockstep" comments were already there; comments do not enforce.
set -euo pipefail

WORKFLOW_DIR=".github/workflows"

# name:version, in the order found
mapfile -t pins < <(
  for f in "$WORKFLOW_DIR"/*.yml; do
    # Only the version: line belonging to a golangci-lint-action step. Track the
    # most recent `uses:` so an unrelated `version:` elsewhere is not picked up.
    awk -v file="$f" '
      /uses:.*golangci-lint-action/ { in_step = 1; next }
      in_step && /version:[[:space:]]*v[0-9]/ {
        match($0, /v[0-9]+\.[0-9]+\.[0-9]+/)
        print file ":" substr($0, RSTART, RLENGTH)
        in_step = 0
        next
      }
      # A new step or job resets the window.
      in_step && /^[[:space:]]*-[[:space:]]*(name|uses):/ { in_step = 0 }
    ' "$f"
  done
)

if [[ "${#pins[@]}" -eq 0 ]]; then
  echo "FAIL: found no golangci-lint-action version pins under $WORKFLOW_DIR"
  echo "If the linter moved to a different action, update $0 to match."
  exit 1
fi

versions=$(printf '%s\n' "${pins[@]}" | sed 's/.*://' | sort -u)
count=$(printf '%s\n' "$versions" | wc -l | tr -d ' ')

if [[ "$count" -ne 1 ]]; then
  echo "FAIL: golangci-lint pins disagree across workflows:"
  printf '  %s\n' "${pins[@]}"
  echo ""
  echo "Every workflow that lints must pin the same version. Otherwise a job"
  echo "that is not run on PRs — the release gate especially — can fail on a"
  echo "finding no PR check would ever have surfaced."
  exit 1
fi

echo "golangci-lint lockstep check passed (${#pins[@]} workflows pinned at $versions)"
