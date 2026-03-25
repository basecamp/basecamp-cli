#!/usr/bin/env bash
# Verify that command group parents don't set RunE (bare invocation shows help).
#
# Shortcut commands that intentionally perform an action AND have subcommands
# are listed in the allowlist below.
set -euo pipefail

COMMANDS_DIR="internal/commands"

# Commands allowed to have RunE + AddCommand (shortcuts, not groups)
ALLOWLIST=(
  NewCardCmd        # shortcut: creates a card
  NewCompletionCmd  # dispatches by shell arg
  NewRecordingsCmd  # shortcut: lists recordings
  NewSearchCmd      # shortcut: performs search
  NewSetupCmd       # wizard entry point
  NewSkillCmd       # renders skill content
  NewTimesheetCmd       # shortcut: shows report
  NewURLCmd             # shortcut: opens URL
  NewAssignmentsCmd     # shortcut: shows assignments
  NewNotificationsCmd   # shortcut: lists notifications
)

is_allowed() {
  local func="$1"
  for allowed in "${ALLOWLIST[@]}"; do
    if [[ "$func" == "$allowed" ]]; then
      return 0
    fi
  done
  return 1
}

violations=0

# Find exported New*Cmd functions that have both AddCommand and RunE
for file in "$COMMANDS_DIR"/*.go; do
  [[ "$file" == *_test.go ]] && continue

  # Extract each NewXxxCmd function body (from "func NewXxxCmd" to next "^func ")
  while IFS= read -r func_name; do
    # Get the function body
    body=$(sed -n "/^func ${func_name}(/,/^func /p" "$file" | sed '$d')

    has_addcommand=$(echo "$body" | grep -Ec 'AddCommand|cmd\.AddCommand' || true)
    has_rune=$(echo "$body" | grep -c 'RunE:' || true)

    if [[ "$has_addcommand" -gt 0 && "$has_rune" -gt 0 ]]; then
      if ! is_allowed "$func_name"; then
        echo "FAIL: $file: $func_name has RunE + AddCommand (group parents should show help, not run)"
        ((violations++))
      fi
    fi
  done < <(grep -Eo 'func (New[A-Za-z]+Cmd)\(' "$file" | sed 's/func //; s/(//' | sort -u)
done

if [[ "$violations" -gt 0 ]]; then
  echo ""
  echo "$violations violation(s). Group commands must not set RunE."
  echo "If this is an intentional shortcut, add it to the ALLOWLIST in $0."
  exit 1
fi

echo "Bare group convention check passed ($(printf '%s\n' "${ALLOWLIST[@]}" | wc -l | tr -d ' ') allowlisted shortcuts)"
