#!/usr/bin/env bash
# test-skill-drift.sh — Run scripts/check-skill-drift.sh against fixture skills
# and a fixture surface, and prove it accepts what the CLI really has and
# refuses what it does not.
#
# The case that matters: the check used to resolve a referenced command to its
# nearest existing ancestor, so "basecamp profile set" resolved to "basecamp
# profile" and passed even though "profile" has no "set" subcommand — which is
# how an invented command reached an operator-facing hint. Resolution now has
# to be exact, except where the leftover words can only be argument values or
# prose. The fixtures below hold both halves: the invented subcommand fails,
# and the argument values and prose that used to need the fallback still pass.
#
# Usage: scripts/test-skill-drift.sh
#        DRIFT_SCRIPT=path/to/check-skill-drift.sh scripts/test-skill-drift.sh

set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
DRIFT_SCRIPT="${DRIFT_SCRIPT:-${here}/check-skill-drift.sh}"
[[ -x "$DRIFT_SCRIPT" ]] || { echo "ERROR: ${DRIFT_SCRIPT} is not an executable script" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
ok() { echo "ok - $*"; }
not_ok() { echo "not ok - $*"; failures=$((failures + 1)); }

# A surface small enough to read whole. It carries one group with subcommands
# and no arguments (profile), one group that takes an argument (recordings),
# one leaf (todos list), and the flag that leaf owns.
cat > "${work}/surface" <<'EOF'
ARG basecamp recordings 00 [type]
CMD basecamp
CMD basecamp profile
CMD basecamp profile list
CMD basecamp recordings
CMD basecamp recordings archive
CMD basecamp todos
CMD basecamp todos list
FLAG basecamp todos list --project type=string
SUB basecamp profile
SUB basecamp recordings
SUB basecamp todos
SUB basecamp profile list
SUB basecamp recordings archive
SUB basecamp todos list
EOF

: > "${work}/baseline"

# What the CLI used to have. Removals reach .surface-breaking before they can
# land, so the check reads it to tell an argument value from a command we
# dropped.
cat > "${work}/breaking" <<'EOF'
CMD basecamp recordings vaults
CMD basecamp todos list stale
EOF

# write_skill <name> <body line>...
write_skill() {
  local name="$1"; shift
  {
    printf -- '---\nname: %s\n---\n\n' "$name"
    printf '%s\n' "$@"
  } > "${work}/${name}.md"
}

# run_check <skill name> [baseline file] — prints output, sets $status
run_check() {
  local name="$1" baseline="${2:-${work}/baseline}"
  out=$("$DRIFT_SCRIPT" "${work}/${name}.md" "${work}/surface" "$baseline" "${work}/breaking" 2>&1)
  status=$?
}

# assert_passes <skill name> <description>
assert_passes() {
  run_check "$1"
  if [ "$status" -eq 0 ]; then ok "$2"; else not_ok "$2 — exited ${status}: ${out}"; fi
}

# assert_fails <skill name> <expected substring> <description>
assert_fails() {
  run_check "$1"
  if [ "$status" -eq 0 ]; then
    not_ok "$3 — the check passed: ${out}"
  elif [[ "$out" != *"$2"* ]]; then
    not_ok "$3 — expected ${2@Q} in: ${out}"
  else
    ok "$3"
  fi
}

# --- The invented subcommand the nearest-ancestor fallback used to swallow ---

write_skill invented 'Bind it: `basecamp profile set agent account_id 123`'
assert_fails invented 'cannot verify' \
  'an invented subcommand under an existing group fails'

# The report says which command was resolved and which word it does not have,
# so the reader is not left to guess what "cannot verify" means.
run_check invented
if [[ "$out" == *'basecamp profile'* && "$out" == *'"set"'* ]]; then
  ok 'the report names the resolved command and the word it does not have'
else
  not_ok "the report names the resolved command and the word it does not have — got: ${out}"
fi

# --- What the fallback existed for, and still has to pass ---

write_skill exact 'Run `basecamp profile list` and `basecamp recordings archive`.'
assert_passes exact 'an exact command passes'

write_skill argvalue 'Run `basecamp recordings cards` to list the cards.'
assert_passes argvalue 'an argument value after a command that takes arguments passes'

write_skill prose 'Run `basecamp todos list` without a project and it fails.'
assert_passes prose 'prose after a leaf command passes'

# --- A subcommand we removed, where an escape would otherwise read it as prose ---

write_skill removed_under_args 'Run `basecamp recordings vaults` for the vaults.'
assert_fails removed_under_args 'removed command' \
  'a removed subcommand fails even where the parent takes an argument'

write_skill removed_under_leaf 'Run `basecamp todos list stale` for the stale ones.'
assert_fails removed_under_leaf 'removed command' \
  'a removed subcommand fails even under a command with no subcommands left'

# --- What already failed, and still has to ---

write_skill unknown 'Run `basecamp bogus thing` for this.'
assert_fails unknown 'command not in surface' \
  'a command with no known prefix fails'

write_skill flagdrift 'Run `basecamp todos list --nope`.'
assert_fails flagdrift 'flag --nope' \
  'a flag the command does not have fails'

write_skill goodflag 'Run `basecamp todos list --project 123`.'
assert_passes goodflag 'a flag the command does have passes'

# --- The baseline still suppresses drift, now for commands too ---

printf 'CMD basecamp profile set\n' > "${work}/baselined"
run_check invented "${work}/baselined"
if [ "$status" -eq 0 ]; then
  ok 'a baselined command reference is not new drift'
else
  not_ok "a baselined command reference is not new drift — exited ${status}: ${out}"
fi

# --- One report per bad command, not one per flag on its line ---

write_skill invented_flag 'Run `basecamp profile set agent --nope --also-nope`.'
run_check invented_flag
count=$(printf '%s\n' "$out" | grep -c '^DRIFT:')
if [ "$count" -eq 1 ]; then
  ok 'a command that fails is not also reported once per flag'
else
  not_ok "a command that fails is not also reported once per flag — ${count} DRIFT lines: ${out}"
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "All skill drift checks passed"
