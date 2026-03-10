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

# Check whether an entry is in the baseline file (avoids Bash 4+ associative arrays).
is_baselined() {
  [ -f "$BASELINE" ] && grep -qxF "$1" "$BASELINE"
}

# Resolve a candidate command path to the longest matching CMD in .surface.
# Prints the match (or nothing). Rejects fallback to bare "basecamp" —
# every extracted candidate has at least one subcommand word, so matching
# only the root means none of the subcommands exist.
resolve_cmd() {
  local candidate="$1"
  read -ra parts <<< "$candidate"
  for ((i=${#parts[@]}; i>=1; i--)); do
    local try="${parts[*]:0:$i}"
    if grep -qxF "CMD $try" "$SURFACE"; then
      # Reject bare root — candidate always has subcommand tokens
      if [ "$try" = "basecamp" ] && [ "${#parts[@]}" -gt 1 ]; then
        return
      fi
      echo "$try"
      return
    fi
  done
}

# Check whether a flag exists on a command or any of its subcommands.
# Descendant matching is intentional: Cobra commands inherit flags and shortcut
# commands delegate to subcommands (e.g. "basecamp cards --in" runs "cards list"
# which has --in). Strict matching would produce false positives.
# Note: uses grep -c instead of grep -q to avoid pipefail + SIGPIPE false negatives.
flag_exists() {
  local cmd="$1" flag="$2"
  local count
  count=$(grep "^FLAG ${cmd} " "$SURFACE" | grep -cF " ${flag} type=" || true)
  [ "$count" -gt 0 ]
}

# Strip YAML frontmatter — trigger keywords (e.g. "basecamp project") are
# natural-language match phrases, not CLI command references.
skill_body=$(awk '/^---$/{n++; next} n>=2' "$SKILL")

# --- Phase 1: Command references ---
# Extract "basecamp <subcommand>..." patterns, resolve to longest matching CMD.
while IFS= read -r candidate; do
  matched=$(resolve_cmd "$candidate")

  if [ -z "$matched" ]; then
    key="CMD ${candidate}"
    if is_baselined "$key"; then
      : # known drift
    else
      echo "DRIFT: command not in surface: $candidate"
      new_drift=$((new_drift + 1))
    fi
    errors=$((errors + 1))
  fi
  cmd_checked=$((cmd_checked + 1))
done < <(echo "$skill_body" | grep -oE 'basecamp( [a-z][-a-z0-9]+)+' | sort -u)

# --- Phase 2: Flag references ---
# For lines with "basecamp <cmd> ... --flag", verify each flag exists on the
# resolved command or one of its subcommands.
tmpfile=$(mktemp)
trap 'rm -f "$tmpfile"' EXIT

echo "$skill_body" | grep -nE 'basecamp [a-z].+--[a-z]' > "$tmpfile" || true

while IFS=: read -r lineno line; do
  # Extract command candidate using BASH_REMATCH to avoid grep|head SIGPIPE
  if [[ "$line" =~ basecamp(\ [a-z][-a-z0-9]+)+ ]]; then
    cmd_candidate="${BASH_REMATCH[0]}"
  else
    continue
  fi

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
      if is_baselined "$key"; then
        : # known drift
      else
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
