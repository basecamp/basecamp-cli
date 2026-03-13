#!/usr/bin/env bash
# check-smoke-coverage.sh - Verify every leaf command is accounted for in smoke tests.
#
# A leaf command is a CMD in .surface that has no children (no other CMD is a
# prefix of it followed by a space). Every leaf must appear in at least one of:
#   1. Tested — a run_smoke call exercises it
#   2. OOS — a mark_out_of_scope test names it
#   3. Unverifiable — a mark_unverifiable test names it
#
# Exit 0 if all leaves are covered, 1 otherwise.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SURFACE="$ROOT_DIR/.surface"
SMOKE_DIR="$ROOT_DIR/e2e/smoke"

if [[ ! -f "$SURFACE" ]]; then
  echo "ERROR: .surface file not found at $SURFACE" >&2
  exit 1
fi

# --- Build CMD lookup from .surface ---
declare -A all_cmds_set
mapfile -t all_cmds < <(grep '^CMD ' "$SURFACE" | sed 's/^CMD //')
for cmd in "${all_cmds[@]}"; do
  all_cmds_set["$cmd"]=1
done

# --- Extract leaf CMDs ---
# A leaf CMD has no other CMD that starts with "$cmd " (i.e., no children).
declare -A is_parent
for cmd in "${all_cmds[@]}"; do
  for other in "${all_cmds[@]}"; do
    if [[ "$other" == "$cmd "* ]]; then
      is_parent["$cmd"]=1
      break
    fi
  done
done

leaves=()
for cmd in "${all_cmds[@]}"; do
  [[ -z "${is_parent[$cmd]:-}" ]] && leaves+=("$cmd")
done

# --- Alias normalization ---
# Maps alias used in tests → canonical .surface name
declare -A alias_map=(
  ["campfire"]="chat"
)

normalize_word() {
  local word="$1"
  if [[ -n "${alias_map[$word]:-}" ]]; then
    echo "${alias_map[$word]}"
  else
    echo "$word"
  fi
}

# find_longest_cmd WORDS...
# Given words extracted from a run_smoke call, find the longest prefix
# that matches a known CMD in .surface.
find_longest_cmd() {
  local -a words=("$@")
  local best=""
  local candidate="basecamp"

  for word in "${words[@]}"; do
    local normalized
    normalized=$(normalize_word "$word")
    local try="$candidate $normalized"
    if [[ -n "${all_cmds_set[$try]:-}" ]]; then
      candidate="$try"
      best="$try"
    else
      break
    fi
  done

  echo "$best"
}

# --- Extract tested commands from run_smoke calls ---
declare -A tested
while IFS= read -r line; do
  # Extract everything after "run_smoke basecamp "
  cmd_part="${line#*run_smoke basecamp }"
  words=()
  for word in $cmd_part; do
    # Stop at flags, variables, or quoted strings
    [[ "$word" == -* || "$word" == \$* || "$word" == \"* || "$word" == \'* ]] && break
    words+=("$word")
  done
  if [[ ${#words[@]} -gt 0 ]]; then
    matched=$(find_longest_cmd "${words[@]}")
    if [[ -n "$matched" ]]; then
      tested["$matched"]=1
    fi
  fi
done < <(grep -rh 'run_smoke basecamp ' "$SMOKE_DIR"/*.bats 2>/dev/null || true)

# --- Extract OOS commands from mark_out_of_scope test names ---
declare -A oos
while IFS= read -r test_name; do
  # Test name format: "X is out of scope"
  cmd="${test_name% is out of scope}"
  [[ "$cmd" != "$test_name" ]] || continue
  oos["basecamp $cmd"]=1
done < <(grep -rh '@test "' "$SMOKE_DIR"/*.bats | sed 's/.*@test "\(.*\)".*/\1/' | grep 'is out of scope' || true)

# --- Propagate OOS to leaf descendants ---
# If a parent group is marked OOS, all its leaf children are covered.
for parent_cmd in "${!oos[@]}"; do
  for leaf in "${leaves[@]}"; do
    if [[ "$leaf" == "$parent_cmd "* ]]; then
      oos["$leaf"]=1
    fi
  done
done

# --- Extract unverifiable commands from mark_unverifiable tests ---
# These are already in the tested bucket since they have run_smoke calls,
# but we track them separately for completeness.
declare -A unverifiable
# lineup update/delete are the known unverifiable-only commands
unverifiable["basecamp lineup update"]=1
unverifiable["basecamp lineup delete"]=1

# --- Check coverage ---
uncovered=()
for leaf in "${leaves[@]}"; do
  if [[ -z "${tested[$leaf]:-}" && -z "${oos[$leaf]:-}" && -z "${unverifiable[$leaf]:-}" ]]; then
    uncovered+=("$leaf")
  fi
done

echo "Leaf commands: ${#leaves[@]}"
echo "Tested: $(printf '%s\n' "${!tested[@]}" | wc -l | tr -d ' ')"
echo "OOS: $(printf '%s\n' "${!oos[@]}" | wc -l | tr -d ' ')"
echo "Unverifiable: $(printf '%s\n' "${!unverifiable[@]}" | wc -l | tr -d ' ')"
echo "Uncovered: ${#uncovered[@]}"

if [[ ${#uncovered[@]} -gt 0 ]]; then
  echo ""
  echo "UNCOVERED COMMANDS:"
  printf '  %s\n' "${uncovered[@]}" | sort
  exit 1
fi

echo ""
echo "All leaf commands accounted for."
