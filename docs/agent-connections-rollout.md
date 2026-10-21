# Agent connections: CLI-first rollout and rollback

This CLI can merge and release before the server changes. It stores the
connection secret as opaque data; no connection API, self-disconnect endpoint,
or server feature switch is required. A second approval **may replace the
existing connection**: it does on the current server, and for any agent whose
kind (personal or team) doesn't have adding connections enabled. Where
Basecamp has enabled it for the agent's kind, personal first and team later,
the approval gains another independent connection instead. Consult
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
   still require individual decisions. This does not promise replay: the existing
   handoff age guard closes eligible requests from before a newly started run
   as `discarded` / `before_this_run`, retaining their ledger rows. Requests
   admitted during a hold are not necessarily individually held. Use a fresh
   trusted event for handoff proof after a restart; inspect historical records
   rather than assuming release will hand them off.
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
6. Decide which one computer answers the agent's Pings (see below), and start
   every other connector for the agent with `--no-pings` once.

## Only one computer answers Pings

A Ping is in no project, so serving projects can't divide Pings between
connections: every connector for the agent sees all of them, and each one
that heard them would answer. Only one computer should answer Pings, and the
person running the agent arranges it; the CLI picks a default, keeps the
choice, and warns about a second it can see on the same computer.

- `--pings` hears Pings and anything else outside a project. `--no-pings`
  hears only the connector's projects.
- With neither, the agent's first connection (its credential is the agent
  client's own secret) hears them, and a connection added after it (its
  secret starts `bcac_`) doesn't. A profile that holds a person's login, a
  bot user's, hears them as every connector did before this build. A run
  given `--project` hears none, as before, and `--pings` with `--project` is
  refused.
- The first start on this build without `--project` saves the answer in the
  profile's state directory (`pings.json`, beside the ledger), as `--pings`
  and `--no-pings` save theirs, and every restart keeps it until one of them
  changes it. A run given `--project` saves nothing, so a narrowed first run
  leaves the choice to the first one that isn't. A profile that heard Pings keeps
  hearing them after a reconnect turns its credential into an added
  connection. An unreadable `pings.json` refuses the start: remove it, then
  choose again with a flag.
- `basecamp connect status` and `basecamp connect doctor` say whether the
  profile's connector answers Pings and why. A connector that hears them
  warns when another for the same agent on this computer does too, an older
  build's included (it hears them unless started with `--project`).
  Connectors on other computers can't be seen.
- Upgrading changes nothing for one connection per agent: it is the first
  connection, or a bot user's login, and it still hears Pings. Where an agent
  already runs several connectors that hear Pings (a bot user on three
  computers, say), choose one, and restart the others with `--no-pings`.

Downgrading drops the choice: older builds ignore `pings.json` and every
connector they run hears Pings unless given `--project`. The file stays, and
the next start on this build reads it again.

State is under `$XDG_STATE_HOME/basecamp/connect` and `connect-shadow` (default
`~/.local/state/basecamp`). New profile directories canonicalize numeric account
aliases so padded account ids cannot bypass the same-profile lock. Legacy
lookup preserves the saved account spelling used by older builds; the adoption
WARN names both paths. On downgrade, restore that saved spelling if padded.
Before adoption, status, doctor and operator commands
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
