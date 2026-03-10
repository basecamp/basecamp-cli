#!/usr/bin/env bash
# Verify that commands and flags referenced in skill files exist in the CLI surface.
# Catches stale skill references: renamed commands, removed flags, etc.
set -euo pipefail

SKILL="${1:-skills/basecamp/SKILL.md}"
SURFACE="${2:-.surface}"
BASELINE="${3:-.surface-skill-drift}"

# Built-in flags not tracked in the surface
BUILTINS="--help --version"

if [ ! -f "$SKILL" ]; then
  echo "ERROR: skill file not found: $SKILL" >&2
  exit 1
fi
if [ ! -f "$SURFACE" ]; then
  echo "ERROR: surface file not found: $SURFACE" >&2
  exit 1
fi

errors=0
cmd_checked=0
flag_checked=0
new_drift=0

# Load baseline of known drift (if any)
declare -A known_drift
if [ -f "$BASELINE" ]; then
  while IFS= read -r entry; do
    [[ -z "$entry" || "$entry" == \#* ]] && continue
    known_drift["$entry"]=1
  done < "$BASELINE"
fi

# Resolve a candidate command path to the longest matching CMD in .surface.
# Prints the match (or nothing).
resolve_cmd() {
  local candidate="$1"
  read -ra parts <<< "$candidate"
  for ((i=${#parts[@]}; i>=1; i--)); do
    local try="${parts[*]:0:$i}"
    if grep -qxF "CMD $try" "$SURFACE"; then
      echo "$try"
      return
    fi
  done
}

# Check whether a flag exists on a command or any of its subcommands.
# Returns 0 if found, 1 if not.
# Note: uses grep -c instead of grep -q to avoid pipefail + SIGPIPE false negatives.
flag_exists() {
  local cmd="$1" flag="$2"
  local count
  count=$(grep "^FLAG ${cmd} " "$SURFACE" | grep -cF " ${flag} type=" || true)
  [ "$count" -gt 0 ]
}

# --- Phase 1: Command references ---
# Extract "basecamp <subcommand>..." patterns, resolve to longest matching CMD.
while IFS= read -r candidate; do
  matched=$(resolve_cmd "$candidate")

  if [ -z "$matched" ]; then
    key="CMD ${candidate}"
    if [ -z "${known_drift[$key]+x}" ]; then
      echo "DRIFT: command not in surface: $candidate"
      new_drift=$((new_drift + 1))
    fi
    errors=$((errors + 1))
  fi
  cmd_checked=$((cmd_checked + 1))
done < <(grep -oE 'basecamp( [a-z][-a-z0-9]+)+' "$SKILL" | sort -u)

# --- Phase 2: Flag references ---
# For lines with "basecamp <cmd> ... --flag", verify each flag exists on the
# resolved command or one of its subcommands.
tmpfile=$(mktemp)
trap 'rm -f "$tmpfile"' EXIT

grep -nE 'basecamp [a-z].+--[a-z]' "$SKILL" > "$tmpfile" || true

while IFS=: read -r lineno line; do
  cmd_candidate=$(echo "$line" | grep -oE 'basecamp( [a-z][-a-z0-9]+)+' | head -1)
  [ -z "$cmd_candidate" ] && continue

  matched=$(resolve_cmd "$cmd_candidate")
  [ -z "$matched" ] && continue

  for flag in $(echo "$line" | grep -oE '\-\-[a-z][-a-z0-9]*' | sort -u); do
    # Skip cobra built-ins
    case " $BUILTINS " in *" $flag "*) continue ;; esac

    flag_checked=$((flag_checked + 1))
    if flag_exists "$matched" "$flag"; then
      : # found
    else
      key="FLAG ${matched} ${flag}"
      if [ -z "${known_drift[$key]+x}" ]; then
        echo "DRIFT: flag ${flag} not found on ${matched} (line ${lineno})"
        new_drift=$((new_drift + 1))
      fi
      errors=$((errors + 1))
    fi
  done
done < "$tmpfile"

# --- Summary ---
baselined=$((errors - new_drift))
if [ $new_drift -gt 0 ]; then
  echo ""
  echo "Found ${new_drift} new skill drift issue(s) (${baselined} baselined)."
  echo "Update ${SKILL} to match the current CLI surface (.surface),"
  echo "or add entries to ${BASELINE} if the drift is intentional."
  exit 1
fi

if [ $baselined -gt 0 ]; then
  echo "Skill drift check passed (${cmd_checked} commands, ${flag_checked} flags; ${baselined} baselined)"
else
  echo "Skill drift check passed (${cmd_checked} commands, ${flag_checked} flags validated)"
fi
