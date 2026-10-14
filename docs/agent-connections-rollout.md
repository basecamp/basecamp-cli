# Agent connections: CLI-first rollout and rollback

This CLI can merge and release before the server changes. It stores the
connection secret as opaque data; no connection API, self-disconnect endpoint,
or server feature switch is required. A second approval **may replace the
existing connection**: it does on the current server, for shared agents, and
for personal agents while adding connections is disabled. Only personal agents
on a server with adding enabled gain another independent connection. Consult
the approval page; do not reconnect merely to upgrade the CLI.

## Upgrade one host

1. Stop **all older connectors for the same account/agent on this host**, normal
   and shadow, including supervisors that could restart them. Keep credentials,
   profiles and `connect.json` unchanged. Do not add a second profile yet.
2. Start the existing profile with this build (`basecamp connect -P <profile>`).
   After its credential proves the saved identity, the first verified run
   renames `<account>-<agent>` to `<account>-<agent>-<profile>` under the old
   instance lock. A WARN names both paths. The entire ledger moves: all filter
   checkpoints, durable hold, held records, decisions and connection history.
   A held connector stays held; migration never calls release.
3. Check `basecamp connect status -P <profile>` and `basecamp connect doctor
   -P <profile>`. Only if intended, use `basecamp connect release -P <profile>`;
   release clears this profile's hold, not other profiles' holds. Held records
   still require individual decisions.
4. For a legacy shadow, run `basecamp connect --shadow -P <profile>` first,
   then stop it. Only then may `basecamp connect shadow promote -P <profile>`
   move that profile's shadow ledger into normal state. Offline promotion
   refuses the unverified legacy shadow instead of assigning it to a stale
   profile. Normal state must not already have a ledger.
5. After server support is confirmed, add other profiles with distinct device
   names and project lists. Each profile has its own ledger, cursor and lock.
   A second run of the same profile is refused. Deliberate project overlap is
   allowed; requests admitted by both can be answered twice. There is no lease
   or cross-profile claim.

State is under `$XDG_STATE_HOME/basecamp/connect` and `connect-shadow` (default
`~/.local/state/basecamp`). Before adoption, status, doctor and operator commands
read the legacy folder without moving it. A working credential that reads back
as a different person cannot adopt. The first verified profile owns the old
state, not necessarily the first profile configured locally. A fresh sibling
starts at the present unless `--since <event-id>` is supplied, and warns where
older state remains. `--since` can replay work; inspect status before choosing
it. If an existing profile already has its own folder, migration does not
replace it or merge ledgers; inspect any remaining legacy state separately.

The consumer namespace intentionally remains `basecamp-connect-<agent>` (and
its shadow counterpart). Checkpoints are local rows in the **profile's isolated
ledger**, not server-side consumers. Changing the namespace would orphan the
adopted cursor; independent ledgers isolate identical keys.

## Mixed builds are not a safe steady state

After adoption an older build can recreate the old folder and run beside the
new one. Its lock cannot see the new profile folder. Its fresh ledger would
lose the old hold and cursor, and could dispatch duplicate work. Never restart
an older supervisor while new connectors for that agent run on the same state
root. Stop old builds before adoption; upgrade all launch commands. Distinct
hosts/state roots remain independent, as before.

## CLI downgrade (one chosen profile, offline)

Stop every connector/supervisor for this account/agent in this state root. Back
up the entire normal and shadow state directories with their permissions (they
contain sensitive request data). Choose **one** profile for the old build;
older builds cannot isolate several profiles for one agent. Ensure neither
legacy destination exists. If one exists, stop: inspect and reconcile manually,
never overwrite or merge SQLite ledgers.

For each applicable group (`connect`, `connect-shadow`), rename the **whole**
`<account>-<agent>-<chosen-profile>` directory back to `<account>-<agent>`.
Do not move just `ledger.db`: journals and migration/promotion state belong to
the same directory. Verify ownership/private permissions. Leave all other
profile directories intact and stopped; do not delete them. Start only the
chosen profile on the older binary and check status/doctor before releasing
any hold. Downgrading without restoring the directory silently leaves held
work/cursors behind and is unsafe. Preserve config/credentials unchanged.

This is a CLI-state rollback, not a server-code rollback. Once own-secret
connections exist, server code predating their authentication is unsupported.
Server switch-off disables **adding** while keeping already-added connections
live; use that switch for server rollback, not a casual code revert.

## Credentials left outside the CLI

Never read or delete a user's `~/.basecamp-agent/credentials.json` during setup.
On an add-capable personal agent it can remain a live secret alongside this
profile; reconnecting a profile can likewise leave its previous connection
live. If the owner confirms an old credential is no longer used, have them
disconnect **that connection** in Basecamp and then remove their unused file.
Do not direct them to Disconnect all. Shared connections are managed in
Adminland, personal ones on My > Agent. A lost one-shot handover can be cleaned
up later by disconnecting the newest connection; its secret was not retained.

## Provenance and proof boundaries

Profile migration/operator fixes and tests reuse Rob Zolkos's
[CLI #901](https://github.com/basecamp/basecamp-cli/pull/901) at `8ead7320`.
Connection copy and lost-handover coverage reuse his
[CLI #900](https://github.com/basecamp/basecamp-cli/pull/900) at `7ba7e962`, adapted
for CLI-first release and Jeremy Daer's October 14 review. The per-connection
logout/revoke guidance from Jeremy's [#851](https://github.com/basecamp/basecamp-cli/pull/851)
is already in the base.

Unit tests use synthetic fake servers and isolated local state. They prove
identity-before-adoption, hold/cursor preservation, sibling isolation, lock
refusal, operator fallback and safe copy—not real server lifecycle behavior.
Real old/new CLI and server flag-off/on HTTP/OAuth/feed scenarios must be run
in the separate isolated integration harness before enabling server adds.
