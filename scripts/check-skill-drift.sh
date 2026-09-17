#!/usr/bin/env bash
# Verify that commands and flags referenced in skill files exist in the CLI surface.
# Catches stale skill references: renamed commands, removed flags, etc.
#
# Usage: scripts/check-skill-drift.sh [skill] [surface] [baseline] [breaking]
#   skill     the SKILL.md to read            (default skills/basecamp/SKILL.md)
#   surface   the CLI surface snapshot        (default .surface)
#   baseline  acknowledged drift              (default .surface-skill-drift)
#   breaking  acknowledged surface removals   (default .surface-breaking)
set -euo pipefail

SKILL="${1:-skills/basecamp/SKILL.md}"
SURFACE="${2:-.surface}"
BASELINE="${3:-.surface-skill-drift}"
# Removals are acknowledged in .surface-breaking before they can land, so it
# is the record of what the CLI used to have and no longer does.
BREAKING="${4:-.surface-breaking}"

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

# Check whether a command path is one the CLI has acknowledged removing.
is_removed() {
  [ -f "$BREAKING" ] && grep -qxF "CMD $1" "$BREAKING"
}

# extract_candidates reads text and prints every "basecamp <word>..." command
# reference in it, one per line.
#
# A word that runs on into a filename or a key — "basecamp upload report.pdf",
# "basecamp config set account_id" — is not a word of the command: the match
# has to end where a word ends, and a ".", "_" or "/" followed by more of a
# name is not an end. So "basecamp upload report.pdf" yields "basecamp upload",
# and "basecamp auth status." at the end of a sentence still yields the whole
# command.
extract_candidates() {
  grep -oE 'basecamp( [a-z][-a-z0-9]+)+([^-a-z0-9._/]|[._/]([^a-zA-Z0-9]|$)|$)' \
    | sed -E 's/[^a-z0-9-]+$//' || true
}

# resolve_cmd prints the longest prefix of a candidate that is a CMD in
# .surface, and nothing when no prefix is one.
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

# verify_cmd decides whether a candidate names a command that exists.
#
# Resolving to the nearest existing ancestor is not enough: "basecamp profile
# set" resolved to "basecamp profile" and passed, though "profile" has no
# "set" subcommand. So a candidate has to resolve EXACTLY, unless the words
# past the resolved command can only be argument values or prose — which is
# the case when the resolved command takes positional arguments (it has ARG
# entries), or is a leaf that takes none (no SUB entries, so no word after it
# could ever name a subcommand).
#
# What is left is the case the old fallback swallowed: a group that has
# subcommands, takes no arguments, and is followed by a word that is not one
# of its subcommands. That word is an invented subcommand, or a reference
# this check cannot verify. Either way it fails.
#
# Those two escapes cannot tell an argument value from a subcommand we
# removed — "basecamp recordings archive" reads as the [type] argument once
# `archive` is gone — so a word that names a removed command fails first,
# whichever escape would otherwise have taken it. They cannot tell one from a
# subcommand that never existed either: under a command that has both
# arguments and subcommands, an invented subcommand still passes as a value.
#
# Prints "<resolved command>|<word it does not have>" and returns 0 when the
# candidate is verified, 1 when it is not. On a failure the resolved command
# is the deepest one that does exist — empty when not even the root does.
verify_cmd() {
  local candidate="$1" matched
  read -ra parts <<< "$candidate"
  matched=$(resolve_cmd "$candidate")
  if [ -z "$matched" ]; then
    echo "|${parts[0]}"
    return 1
  fi

  read -ra found <<< "$matched"
  local depth=${#found[@]}
  if [ "$depth" -eq "${#parts[@]}" ]; then
    echo "$matched|"
    return 0
  fi

  local word="${parts[$depth]}"
  # A word that names a command we removed is drift whatever else it could
  # be, so it is refused before the escapes below could read it as prose.
  if is_removed "${matched} ${word}"; then
    echo "${matched}|${word}"
    return 1
  fi
  # Leftover words are argument values when the command takes arguments.
  if grep -qE "^ARG ${matched} [0-9]{2} " "$SURFACE"; then
    echo "$matched|"
    return 0
  fi
  # Leftover words are prose when the command has no subcommands to name.
  if ! grep -qE "^SUB ${matched} [a-z]" "$SURFACE"; then
    echo "$matched|"
    return 0
  fi

  echo "${matched}|${word}"
  return 1
}

# drift_key prints the baseline entry that acknowledges a failed candidate,
# given verify_cmd's output for it.
drift_key() {
  local candidate="$1" result="$2"
  local resolved="${result%%|*}" word="${result#*|}"
  if [ -z "$resolved" ] || [ "$resolved" = "basecamp" ]; then
    # Nothing past "basecamp" names a command: the whole reference is the
    # drift, and the whole reference is what a baseline entry names.
    echo "CMD ${candidate}"
  else
    echo "CMD ${resolved} ${word}"
  fi
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
# Extract "basecamp <subcommand>..." patterns and verify each resolves.
while IFS= read -r candidate; do
  [ -z "$candidate" ] && continue
  status=0
  result=$(verify_cmd "$candidate") || status=$?

  if [ "$status" -ne 0 ]; then
    resolved="${result%%|*}"
    word="${result#*|}"
    key=$(drift_key "$candidate" "$result")
    if [ -z "$resolved" ] || [ "$resolved" = "basecamp" ]; then
      message="command not in surface: $candidate"
    elif is_removed "${resolved} ${word}"; then
      message="removed command: \"$candidate\" — ${resolved} no longer has \"${word}\""
    else
      message="cannot verify \"$candidate\": ${resolved} has no subcommand \"${word}\""
    fi
    if is_baselined "$key"; then
      : # known drift
    else
      echo "DRIFT: $message (baseline entry: $key)"
      new_drift=$((new_drift + 1))
    fi
    errors=$((errors + 1))
  fi
  cmd_checked=$((cmd_checked + 1))
done < <(echo "$skill_body" | extract_candidates | sort -u)

# --- Phase 2: Flag references ---
# For lines with "basecamp <cmd> ... --flag", verify each flag exists on the
# resolved command or one of its subcommands.
tmpfile=$(mktemp)
trap 'rm -f "$tmpfile"' EXIT

echo "$skill_body" | grep -nE 'basecamp [a-z].+--[a-z]' > "$tmpfile" || true

while IFS=: read -r lineno line; do
  # The first command reference on the line, read whole so no pipe is cut
  # short under pipefail.
  candidates=$(printf '%s\n' "$line" | extract_candidates)
  cmd_candidate="${candidates%%$'\n'*}"
  [ -z "$cmd_candidate" ] && continue

  status=0
  result=$(verify_cmd "$cmd_candidate") || status=$?
  if [ "$status" -ne 0 ]; then
    # New drift was reported once, in phase 1, and is no base for a flag
    # check. Acknowledged drift still has its flags checked, against the
    # deepest command that does exist, as they always were.
    is_baselined "$(drift_key "$cmd_candidate" "$result")" || continue
  fi
  matched="${result%%|*}"
  [ -z "$matched" ] && continue

  for flag in $(echo "$line" | grep -oE -- '--[a-z][-a-z0-9]*' | sort -u); do
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
  echo "or add the entry each DRIFT line names to ${BASELINE} if the drift is intentional —"
  echo "a hidden command is never in .surface, and has to be acknowledged that way."
  exit 1
fi

if [ $baselined -gt 0 ]; then
  echo "Skill drift check passed (${cmd_checked} commands, ${flag_checked} flags; ${baselined} baselined)"
else
  echo "Skill drift check passed (${cmd_checked} commands, ${flag_checked} flags validated)"
fi
