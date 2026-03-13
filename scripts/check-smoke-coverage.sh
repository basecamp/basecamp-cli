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

if ((BASH_VERSINFO[0] < 4)); then
  echo "ERROR: check-smoke-coverage.sh requires Bash 4+ (found ${BASH_VERSION})" >&2
  echo "  On macOS: brew install bash" >&2
  exit 1
fi

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

# --- Extract always-unverifiable commands from test metadata ---
# An "always-unverifiable" test calls mark_unverifiable but never run_smoke.
# These are tests where the command *should* work but cannot be exercised due
# to environment limitations (e.g., lineup create returns no ID to chain).
# Derived mechanically — no hardcoded list.
declare -A unverifiable
while IFS= read -r bats_file; do
  # Split file into test blocks; check each for mark_unverifiable without run_smoke
  in_test=0
  test_name=""
  has_run_smoke=0
  has_mark_unverifiable=0
  while IFS= read -r line; do
    if [[ "$line" =~ ^@test\ \"(.*)\" ]]; then
      # Emit previous test if it qualifies
      if [[ $in_test -eq 1 && $has_mark_unverifiable -eq 1 && $has_run_smoke -eq 0 && -n "$test_name" ]]; then
        # Extract command from test name using longest-prefix match
        # shellcheck disable=SC2206
        name_words=($test_name)
        matched=$(find_longest_cmd "${name_words[@]}")
        [[ -n "$matched" ]] && unverifiable["$matched"]=1
      fi
      test_name="${BASH_REMATCH[1]}"
      in_test=1
      has_run_smoke=0
      has_mark_unverifiable=0
    elif [[ $in_test -eq 1 ]]; then
      [[ "$line" == *run_smoke* ]] && has_run_smoke=1
      [[ "$line" == *mark_unverifiable* ]] && has_mark_unverifiable=1
    fi
  done < "$bats_file"
  # Emit last test in file
  if [[ $in_test -eq 1 && $has_mark_unverifiable -eq 1 && $has_run_smoke -eq 0 && -n "$test_name" ]]; then
    name_words=($test_name)
    matched=$(find_longest_cmd "${name_words[@]}")
    [[ -n "$matched" ]] && unverifiable["$matched"]=1
  fi
done < <(find "$SMOKE_DIR" -name '*.bats' -type f)

# --- Check coverage ---
uncovered=()
for leaf in "${leaves[@]}"; do
  if [[ -z "${tested[$leaf]:-}" && -z "${oos[$leaf]:-}" && -z "${unverifiable[$leaf]:-}" ]]; then
    uncovered+=("$leaf")
  fi
done

echo "Leaf commands: ${#leaves[@]}"
echo "Tested: ${#tested[@]}"
echo "OOS: ${#oos[@]}"
echo "Unverifiable: ${#unverifiable[@]}"
echo "Uncovered: ${#uncovered[@]}"

if [[ ${#uncovered[@]} -gt 0 ]]; then
  echo ""
  echo "UNCOVERED COMMANDS:"
  printf '  %s\n' "${uncovered[@]}" | sort
  exit 1
fi

echo ""
echo "All leaf commands accounted for."
