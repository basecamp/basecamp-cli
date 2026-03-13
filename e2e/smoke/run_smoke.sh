#!/usr/bin/env bash
# run_smoke.sh - Orchestrator for the pre-release smoke suite.
#
# Usage:
#   BASECAMP_PROFILE=dev ./e2e/smoke/run_smoke.sh
#   BASECAMP_TOKEN=<token> ./e2e/smoke/run_smoke.sh
#
# Runs Level 0 (read-only) tests in parallel, then Level 1+ serially.
# Pass/fail is determined by bats exit codes.
# Traces (QA_TRACE_DIR) record coverage gaps (unverifiable, out-of-scope).

set -euo pipefail

SMOKE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SMOKE_DIR/../.." && pwd)"

# Require auth: either a profile (carries token + base_url + account) or a bare token
if [[ -z "${BASECAMP_PROFILE:-}" && -z "${BASECAMP_TOKEN:-}" ]]; then
  echo "Error: BASECAMP_PROFILE or BASECAMP_TOKEN must be set" >&2
  echo "Usage: BASECAMP_PROFILE=dev $0" >&2
  echo "       BASECAMP_TOKEN=<token> $0" >&2
  exit 1
fi

# Require bats
if ! command -v bats >/dev/null 2>&1; then
  echo "Error: bats not found. Install bats-core to run smoke tests." >&2
  exit 1
fi

# Set up trace directory
export QA_TRACE_DIR="${QA_TRACE_DIR:-$ROOT_DIR/tmp/qa-traces}"
rm -rf "$QA_TRACE_DIR"
mkdir -p "$QA_TRACE_DIR"

export BASECAMP_NO_KEYRING=1
[[ -n "${BASECAMP_PROFILE:-}" ]] && export BASECAMP_PROFILE
[[ -n "${BASECAMP_TOKEN:-}" ]] && export BASECAMP_TOKEN
[[ -n "${BASECAMP_LAUNCHPAD_URL:-}" ]] && export BASECAMP_LAUNCHPAD_URL
export PATH="$ROOT_DIR/bin:$PATH"

# Detect parallelism — bats -j requires GNU parallel, not moreutils parallel.
# Fall back to serial if GNU parallel isn't available.
jobs=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 1)
if ! parallel --will-cite true ::: true 2>/dev/null; then
  jobs=1
fi

echo "=== Smoke Suite ==="
echo "Traces: $QA_TRACE_DIR"
echo ""

bats_failures=0

# Level 0: Read-only (parallel)
echo "--- Level 0: Read-only tests (parallel, $jobs jobs) ---"
level0=(
  "$SMOKE_DIR"/smoke_core.bats
  "$SMOKE_DIR"/smoke_projects.bats
  "$SMOKE_DIR"/smoke_todos_read.bats
  "$SMOKE_DIR"/smoke_todolistgroups.bats
  "$SMOKE_DIR"/smoke_files_read.bats
  "$SMOKE_DIR"/smoke_messages_read.bats
  "$SMOKE_DIR"/smoke_cards_read.bats
  "$SMOKE_DIR"/smoke_misc_read.bats
  "$SMOKE_DIR"/smoke_reports.bats
  "$SMOKE_DIR"/smoke_communication.bats
  "$SMOKE_DIR"/smoke_checkins.bats
  "$SMOKE_DIR"/smoke_schedule.bats
  "$SMOKE_DIR"/smoke_config_local.bats
)
level0_exist=()
for f in "${level0[@]}"; do
  [[ -f "$f" ]] && level0_exist+=("$f")
done
if [[ ${#level0_exist[@]} -gt 0 ]]; then
  bats -j "$jobs" "${level0_exist[@]}" || bats_failures=$((bats_failures + 1))
fi

# Level 1: Project-scoped mutations (parallel, each file isolates its own project)
echo ""
echo "--- Level 1: Mutation tests (parallel, $jobs jobs) ---"
level1=(
  "$SMOKE_DIR"/smoke_todos_write.bats
  "$SMOKE_DIR"/smoke_messages_write.bats
  "$SMOKE_DIR"/smoke_files_write.bats
  "$SMOKE_DIR"/smoke_cards_write.bats
  "$SMOKE_DIR"/smoke_comments.bats
  "$SMOKE_DIR"/smoke_campfire.bats
  "$SMOKE_DIR"/smoke_webhooks.bats
  "$SMOKE_DIR"/smoke_assign.bats
  "$SMOKE_DIR"/smoke_lineup.bats
  "$SMOKE_DIR"/smoke_communication_write.bats
  "$SMOKE_DIR"/smoke_misc_write.bats
  "$SMOKE_DIR"/smoke_tools.bats
  "$SMOKE_DIR"/smoke_cards_column_write.bats
  "$SMOKE_DIR"/smoke_checkins_write.bats
  "$SMOKE_DIR"/smoke_todolistgroups_write.bats
  "$SMOKE_DIR"/smoke_schedule_write.bats
)
level1_exist=()
for f in "${level1[@]}"; do
  [[ -f "$f" ]] && level1_exist+=("$f")
done
if [[ ${#level1_exist[@]} -gt 0 ]]; then
  bats -j "$jobs" "${level1_exist[@]}" || bats_failures=$((bats_failures + 1))
fi

# Level 2+: Account-scoped and lifecycle (serial)
echo ""
echo "--- Level 2+: Account-scoped tests (serial) ---"
level2=(
  "$SMOKE_DIR"/smoke_projects_write.bats
  "$SMOKE_DIR"/smoke_account.bats
  "$SMOKE_DIR"/smoke_lifecycle.bats
)
for f in "${level2[@]}"; do
  [[ -f "$f" ]] && { bats "$f" || bats_failures=$((bats_failures + 1)); }
done

echo ""

# Report coverage gaps from traces
if [[ -f "$QA_TRACE_DIR/traces.jsonl" ]]; then
  unverified=$(jq -r 'select(.status == "unverifiable") | .test' "$QA_TRACE_DIR/traces.jsonl" 2>/dev/null | wc -l | tr -d ' ')

  if [[ "$unverified" -gt 0 ]]; then
    echo "Coverage gaps: $unverified unverifiable"

    # Check allowlist (strip inline comments and blank lines before matching)
    allowlist="$SMOKE_DIR/.qa-allowlist"
    blocking_unverified=0
    while IFS= read -r test_name; do
      if ! sed 's/ *#.*//' "$allowlist" 2>/dev/null | grep -v '^$' | grep -qxF "$test_name"; then
        blocking_unverified=$((blocking_unverified + 1))
        echo "  - $test_name (not allowlisted)"
      fi
    done < <(jq -r 'select(.status == "unverifiable") | .test' "$QA_TRACE_DIR/traces.jsonl" 2>/dev/null)

    if [[ "$blocking_unverified" -gt 0 ]]; then
      bats_failures=$((bats_failures + 1))
    fi
  fi
fi

if [[ "$bats_failures" -gt 0 ]]; then
  echo "BLOCKED: $bats_failures failure(s)"
  exit 1
fi

echo "All smoke tests passed."
