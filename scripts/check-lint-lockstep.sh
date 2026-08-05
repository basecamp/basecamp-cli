#!/usr/bin/env bash
# Verify every workflow lints with the same golangci-lint version.
#
# A stale pin here does not fail a PR — it fails whichever job is the odd one
# out, at the moment that job runs. release.yml sat on v2.9.0 while test.yml and
# security.yml moved to v2.11.1, and the drift surfaced as a gosec G115 false
# positive that blocked a release tag after every PR check had gone green. The
# "keep in lockstep" comments were already there; comments do not enforce.
#
# Every golangci-lint-action step must carry a version, not just agree with the
# others. An unpinned step resolves to whatever the action defaults to, which
# drifts on its own schedule and would rebuild exactly the release-only mismatch
# this exists to prevent — so a missing pin is a failure, not a skip.
set -euo pipefail

WORKFLOW_DIR=".github/workflows"

# One line per golangci-lint-action step: "<file>:<version>", or
# "<file>:UNPINNED" when the step declares no version.
mapfile -t pins < <(
  for f in "$WORKFLOW_DIR"/*.yml; do
    awk -v file="$f" '
      function close_step() {
        if (in_step) { print file ":UNPINNED"; in_step = 0 }
      }
      # A new list item ends the previous step, pinned or not.
      /^[[:space:]]*-[[:space:]]/ { close_step() }
      /uses:.*golangci-lint-action/ { in_step = 1; next }
      in_step && /^[[:space:]]*version:[[:space:]]*v[0-9]/ {
        match($0, /v[0-9]+\.[0-9]+\.[0-9]+/)
        print file ":" substr($0, RSTART, RLENGTH)
        in_step = 0
        next
      }
      # Dedenting to a new job or top-level key also ends the step.
      in_step && /^[[:space:]]{0,4}[A-Za-z_-]+:/ { close_step() }
      END { close_step() }
    ' "$f"
  done
)

if [[ "${#pins[@]}" -eq 0 ]]; then
  echo "FAIL: found no golangci-lint-action steps under $WORKFLOW_DIR"
  echo "If the linter moved to a different action, update $0 to match."
  exit 1
fi

unpinned=$(printf '%s\n' "${pins[@]}" | grep ':UNPINNED$' || true)
if [[ -n "$unpinned" ]]; then
  echo "FAIL: golangci-lint-action step with no version pin:"
  printf '%s\n' "$unpinned" | sed 's/:UNPINNED$//; s/^/  /'
  echo ""
  echo "An unpinned step takes whatever the action defaults to and drifts on"
  echo "its own schedule, which is the mismatch this check exists to prevent."
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

echo "golangci-lint lockstep check passed (${#pins[@]} steps pinned at $versions)"
