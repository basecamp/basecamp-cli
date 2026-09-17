---
name: basecamp-connect
description: |
  Connect a Basecamp agent to this computer and manage the local agent
  connector's setup: the agent's credential (basecamp auth agent connect),
  connect.json (who may drive the agent, which project routes to which
  directory), and readiness (basecamp connect setup). Explains every setup
  result and failure. Starting and supervising the connector is not in this
  skill yet.
  Use when asked to connect an agent, set up or change the connector, add or
  remove a project, change who can drive the agent, or find out why setup
  says the connector is not ready.
triggers:
  - /basecamp-connect
  - connect an agent
  - set up the connector
  - basecamp connect setup
  - basecamp auth agent connect
  - connect.json
  - route a project to a directory
  - who can drive the agent
  - connector not ready
---

# Basecamp connector: connect an agent and manage its setup

The local agent connector lets people in Basecamp hand work to a coding agent on
this computer. It listens to the account's event feed **as a Basecamp agent**,
admits what a trusted person asks of that agent, and runs the work in the local
directory the project is routed to. The agent replies in Basecamp as itself.

You manage it for the person. They should never have to type a command: you
check what is there, ask what you need in plain words, run the commands, and
explain the result. This skill is the reference you do that from.

## Two commands, two jobs

| Command | Owns | Run it when |
|---------|------|-------------|
| `basecamp auth agent connect -P '<profile>'` | The agent's **credential**, stored under a CLI profile | The profile does not exist yet, or the person agrees to replace its Agent credential |
| `basecamp connect setup -P '<profile>'` | **Policy and readiness**: connect.json and the checks | First setup after the credential, and every change to trust or routes |

- **Order on first setup:** connect, confirm who the credential is, then setup.
  Setup does not obtain a credential.
- **Setup never touches the credential.** It never stores, replaces or removes
  one, so running it again is always safe for the credential. (An access token
  that expires is minted or renewed as by any command.)
- **Changing the credential means running connect again, not setup.** Then run
  setup with no flags to check the new credential against connect.json.
  Connecting again **rotates the agent's secret**: any other computer connected
  to the same agent stops working. Do it only when the person agrees.
- `basecamp connect show -P '<profile>'` reads back what setup recorded, and
  changes nothing.
- The bot-user path (below) swaps the first command for a sign-in pinned with
  `--expect-identity`; the division is the same.

## Rules without exceptions

**Credentials**

- Never ask the person to paste a token, secret or password into the
  conversation, and never put one in a flag, an environment variable or a file.
- Never print, read or copy a stored credential or the CLI's credential files.
- Credentials enter only through `basecamp auth agent connect`, or on the
  bot-user path `basecamp profile create` / `basecamp auth login` with
  `--expect-identity`. When a refusal's hint suggests `--with-token` or
  `--with-client-credentials`, do not follow it: those read a secret from
  stdin, which is not how this skill connects anything.
- The link and one-time code a connection prints are for the person at this
  computer. Show them in this conversation only; never post them to Basecamp,
  chat, a file or anywhere else. Whoever approves that code chooses which agent
  this computer acts as.
- Setup and the connection refuse to run while `BASECAMP_TOKEN` is set. Tell the
  person to unset it in their shell; do not set, print or work around it.

**Identity.** Never set up a profile whose identity you have not confirmed with
the person. Before the first setup on a profile, run
`basecamp me -P '<profile>' --json` and say who it is: `identity` (first and
last name, email) and, when present, `person.name` and `person.id`. Go on only
when the person says that is the agent. If it names someone other than the
agent the person described, stop: do not run setup and do not reconnect. Tell
the person who the credential is and let them decide. After setup, check
`data.agent_person_id` matches `person.id` when `me` reported one.

**Shell quoting.** Two kinds of value go into commands, and each has one rule:

- **Numeric ids** (project, person, account and identity ids) go in bare, as
  digits only. Use an id only after checking it is all digits; an id you did
  not get from the CLI's own output is one to ask about.
- **Every other value** goes in single quotes: profile names, directories, class
  labels, anything the person typed. Write a single quote inside a value as
  `'\''`. Single quotes stop `~` expanding, so write a directory as an absolute
  path. Fixed words from this skill (`operator`, `spawn`, `90m`) need no quotes.

Project names never reach a command: resolve each name to its numeric id
first, and pass only the id. For example the directory
`/home/me/Work/Q3 $launch` for project 222 is
`--route '222=/home/me/Work/Q3 $launch'`.

**Interactive logins.** `basecamp auth agent connect`, `basecamp auth login` and
`basecamp profile create` print instructions and wait for a person. Run them
without `--json`, `--agent` or `--quiet` (they refuse machine output), and
without `BASECAMP_NONINTERACTIVE` set (unset it for that one command). Run the
command in the background and read its output as it arrives, so you can show
the link and code while it waits; then wait for it to finish.

## Where things live

| What | Where |
|------|-------|
| Credential | The CLI's credential store, under the profile. `basecamp auth status -P '<profile>' --json` describes it (see Inspecting). Never open it. |
| connect.json | `$XDG_CONFIG_HOME/basecamp/connect/<profile>/connect.json`, default `~/.config/basecamp/connect/<profile>/connect.json`. Setup's JSON result gives the exact `path`. |
| Setup lock | `.connect.lock` beside connect.json. One setup per profile at a time. |
| Connector runtime state (ledger, checkpoint, lock) | Does not exist yet: it comes with the connector run (card 24, behind step 21). Do not look for it. |

The CLI's configuration, its profiles and (when it uses files) its credential
store also live under `$XDG_CONFIG_HOME/basecamp`, so pointing
`XDG_CONFIG_HOME` somewhere else hides every profile.

connect.json holds ids, a trust mode and directory paths, and no credential.
Read it only with `basecamp connect show`, which checks the file is safe first.

## connect.json

connect.json is the only authority for who may drive the agent and which
directory a project's work runs in. Nothing read from Basecamp adds a route or
widens trust.

```json
{
  "version": 1,
  "profile": "agent",
  "account_id": "999",
  "agent": { "person_id": 4001, "kind": "agent" },
  "trust": { "mode": "operator", "operator_id": 1001 },
  "projects": {
    "222": { "path": "/home/me/Work/app", "class": "internal", "watch_completions": true }
  },
  "driver": "spawn",
  "concurrency": 2,
  "deadline": "45m0s",
  "worktrees": false
}
```

| Field | Controls | Changed with |
|-------|----------|--------------|
| `profile`, `account_id` | The profile holding the agent's credential and the account it belongs to | Fixed at first setup. Another account means removing the file. |
| `agent.person_id`, `agent.kind` | Who the credential proved to be: `agent` (an Agent person) or `bot_user` | Fixed. The connector refuses to act if the credential stops matching. |
| `agent.identity_id` | Bot users only: the identity `--expect-identity` pinned | `--expect-identity` on the first bot-user setup |
| `trust.mode` | Who may drive the agent: `operator`, `allowlist` or `project` | `--trust` |
| `trust.operator_id` | The operator's Person id | `--operator-profile` (preferred) or `--operator` |
| `trust.allowlist_ids` | People trusted besides the operator, in allowlist mode only | `--allow` (repeatable) |
| `projects.<id>.path` | The directory that project's work runs in: absolute, symlinks resolved | `--route '<id>=<dir>'`, `--remove-route <id>` |
| `projects.<id>.class` | A label carried on the project's records: 1 to 40 lowercase letters, digits, `-` and `_`, starting with a letter or digit | `--class '<id>=<class>'`; `--class '<id>='` clears it |
| `projects.<id>.watch_completions` | Every trusted completion in the project reaches the agent, without assigning it | `--watch-completions <id>`, `--no-watch-completions <id>` |
| `driver` | How workers are run: `spawn` (default) or `acp` | `--driver` |
| `concurrency` | Workers at once, 1 to 32 (default 2) | `--concurrency` |
| `deadline` | Time limit per task, 1m to 24h (default 45m) | `--deadline 90m` |
| `worktrees` | Each task gets its own git worktree of the routed directory | `--worktrees`, `--worktrees=false` |

**Never edit connect.json by hand.** It is the trust anchor: setup verifies
every person and route before writing it, writes it owner-only, and parses it
strictly (an unknown or misspelled key, a key given twice, or a loose permission
makes it refused). Every change goes through setup.

## Trust, in a sentence each

Explain the modes this way when you ask:

- **operator** (default): only the operator can drive the agent.
- **allowlist**: the operator plus specific people you name.
- **project**: the operator plus anyone in the project who is not a client.

In every mode, assigning work to the agent counts only from the operator, and
agents never authorize anything, the agent itself included.

**The operator** is the person the agent takes instructions from. Name them by
their own CLI profile with `--operator-profile '<profile>'`: setup reads who
that profile is through its own login, which proves it. `--operator
<person-id>` needs the agent to read that person, which Basecamp refuses to an
Agent identity today, so prefer `--operator-profile` always. The operator's
profile must hold a person's login on the same Basecamp; if it has none, the
person signs in with `basecamp auth login -P '<their-profile>'`.

## Inspecting the current setup

Check before you change anything, and before you ask the person anything you
could look up:

1. **Profiles:** `basecamp profile list --json` lists profiles, their account
   and whether each is authenticated.
2. **Credential:** `basecamp auth status -P '<profile>' --json`.
   - An `unknown profile` error: the profile does not exist. The connection
     creates it.
   - `authenticated` false and no `oauth_type`: nothing usable is stored.
   - `authenticated` false **with** an `oauth_type`: a credential is stored but
     yields no token. Do not connect over it; tell the person what kind it is
     and ask.
   - `oauth_type` `agent`: an Agent person. Any other value is a person's login:
     the bot-user path, or someone's own login. Ask which.
   - `storage` `env`: the answer describes `BASECAMP_TOKEN`, not the profile.
     Have the person unset it and check again.
3. **Who it is:** `basecamp me -P '<profile>' --json` (see Identity above).
4. **Policy:** `basecamp connect show -P '<profile>' --json` prints connect.json
   as setup recorded it, changing nothing and making no request. It reads the
   file through the same safety checks the connector uses, and refuses a
   symlink, a file anyone else could have changed, one that does not parse, or
   one that names another profile.
   A `not_found` error means the profile has never been set up. **Never read
   connect.json directly** (no `cat`, no file read): that skips those checks.
   To tell the person which projects are routed, look each id up under the
   agent's profile (`basecamp projects show <id> -P '<profile>' --json`); if that
   is refused, use the operator's profile. Say names, not ids.
5. **Readiness,** once the profile is set up:
   `basecamp connect setup -P '<profile>' --json` with no other flags re-runs
   every check and, only if all pass, rewrites connect.json with what it already
   holds. It changes nothing else, and writes nothing when a check fails. There
   is no separate dry-run flag; do not invent one.

## First-time setup, guided

Work through these in order, asking only what you cannot find out.

**1. Profile and credential.** Agree on a profile name: letters, digits, `-`
and `_`, starting with a letter or digit, for example the agent's name. Inspect it (steps 1 to 3 above).

- **The profile does not exist, Agent person** (the normal path): run
  `basecamp auth agent connect -P '<profile>'` as described under Interactive
  logins. Show the person the link and one-time code. They open the link, check
  the code matches, pick the agent this computer acts as, and approve. Add
  `--no-browser` when the person is on another device.
- **The profile exists with an Agent credential:** do not connect again. Confirm
  its identity with `basecamp me`. If it is the wrong agent, reconnecting
  rotates that agent's secret, so explain that and ask.
- **The profile exists with anything else:** ask. Never connect an agent over a
  person's login.
- **Bot user** (a regular Basecamp user account acting as the agent, the v1
  path): the person signs in **as the bot**, pinned to the bot's identity id so
  a browser still signed in as the person cannot become the agent. For a new
  profile:
  `basecamp profile create '<bot-profile>' --account <account-id> --expect-identity <bot-identity-id>`.
  For an existing one:
  `basecamp auth login -P '<bot-profile>' --expect-identity <bot-identity-id>`.
  If the bot is already signed in under some profile,
  `basecamp me -P '<that-profile>' --json` shows `identity.id`; confirm the name
  and email with the person before using it. Pass the same `--expect-identity`
  to the first setup.

Then confirm the identity (`basecamp me`) with the person before going on.

**2. Operator.** Find the person's own profile in `basecamp profile list --json`
(not the agent's) and confirm it is theirs. Use `--operator-profile`.

**3. Trust mode.** Explain the three modes in a sentence each and ask. Default
to `operator`. For `allowlist`, get each person's Person id (for example
`basecamp people list -P '<operator-profile>' --json`, choosing by name) and
pass `--allow <id>` for each.

**4. Projects, by name.** Never ask for a project id.

- List the projects: `basecamp projects list -P '<agent-profile>' --json`. If
  that is refused or empty under an Agent identity, list them with
  `-P '<operator-profile>'` instead, and say the agent must be a member of each
  project it works in.
- Show the names, let the person choose, and map each choice to its numeric
  `id` yourself. When a name matches more than one project, ask which.
- For each project ask which local directory its work runs in. Check the
  directory exists. If worktrees are wanted, check it is a git repository
  (`git -C '<dir>' rev-parse --show-toplevel`).
- Offer `--watch-completions` only when the person wants the agent to act on
  every completed to-do or card in a project without being assigned. Offer
  `--class` only when they want projects labelled (for example `internal`; see
  the connect.json table for what a label may contain).
  Leave `--driver`, `--concurrency` and `--deadline` at their defaults unless
  asked.

**5. Confirm, then run setup.** Say back in plain words: the agent, the
operator, the trust mode, and each project name with its directory. Then run,
quoting values by the Shell quoting rule:

```bash
basecamp connect setup -P '<profile>' --operator-profile '<operator-profile>' \
  --route '<project-id>=<dir>' --route '<project-id>=<dir>' --json
```

adding `--trust`, `--allow`, `--watch-completions`, `--class` or `--worktrees`
as chosen, and on the bot-user path `--expect-identity <bot-identity-id>`.

**6. Read the result** (next section) and tell the person what it means. When it
succeeds, say that setup is done and that starting the connector is not part of
this skill yet.

## Changing the setup later

Run setup again with only what changes; everything not passed is kept. Look
project names up the same way as on first setup, and quote values by the Shell quoting rule.

| To | Run |
|----|-----|
| Add a project, or move it to another directory | `basecamp connect setup -P '<profile>' --route '<id>=<dir>' --json` |
| Remove a project | `basecamp connect setup -P '<profile>' --remove-route <id> --json` |
| Watch, or stop watching, a project's completions | `--watch-completions <id>` / `--no-watch-completions <id>` |
| Label a project, or clear its label | `--class '<id>=<class>'` / `--class '<id>='` |
| Trust only the operator, or project members | `--trust operator` / `--trust project` (leaving allowlist mode drops the list) |
| Trust specific people | `--allow <person-id>` for each; the list you pass **replaces** the old one, so pass everyone who stays |
| Change the operator | `--operator-profile '<profile>'` |
| Change workers | `--driver`, `--concurrency`, `--deadline`, `--worktrees` / `--worktrees=false` |
| Replace the agent's credential (only with the person's consent: it rotates the secret) | `basecamp auth agent connect -P '<profile>'`, then setup with no flags to re-check |

A class or watch setting needs the project routed first, in the same run or an
earlier one. A project cannot be routed and removed in one run. The last route
cannot be removed on its own: with no routes the connector is not ready, so
setup writes nothing. Say so, and ask what the person wants instead.

Some changes setup refuses on purpose, because connect.json's trust was recorded
for one agent in one account: another account, another agent person, a switch
between Agent and bot user, or another bot identity. Each refusal names
connect.json. The way through is to remove that file and set the profile up
afresh, which drops every route and trust setting. Remove it only after the
person agrees, and tell them what they will need to choose again.

## Reading setup's result

With `--json`, success is `{"ok": true, "data": {...}, "summary": ...}` with
`data.ready` true, `data.written` true, the file `path`, `agent_person_id`,
`agent_kind`, `operator_id`, `trust_mode`, `routes` (a count) and `checks`
(each `name`, `status`, `message`, sometimes `hint`). A `warn` check is usable;
mention it.

A failure is `{"ok": false, "error": ..., "code": ..., "hint": ...}` and a
non-zero exit. **When setup fails, connect.json was not written**: the previous
file, if any, is unchanged. Explain the `error` in plain words. Follow the
`hint` only when it is a step these rules allow. Always read `code`, not only
the exit status: exit 7 is shared.

| `code` (exit) | Means | Next step |
|---------------|-------|-----------|
| `usage` (1) | Input refused: a bad flag value, no operator on a first setup, a missing directory, a class or watch setting on an unrouted project, `--expect-identity` on an Agent credential, a person refused by trust (an Agent, a client, the agent itself, or unreadable), or connect.json itself unusable | Fix the input the message names and run again. |
| `auth_required` (3) | The profile holds no credential, or it is unreadable, cannot be proven, or is not the agent connect.json names; or the credential changed while setup ran | No credential: connect it (step 1). Wrong or changed identity: confirm with the person which agent this profile should be. Changed mid-run: run setup again. |
| `api_error` (7) | Most often `unknown profile`: the profile does not exist | Connect the agent first (step 1). |
| `not_ready` (7) | A readiness check failed. `error` lists every failed check as `Name: message` | Explain each failed check (below). |
| `busy` (5) | Another command is using this profile's credential or setup lock | Nothing is wrong. Run setup again when it has finished. |
| `lock_unavailable` (5) | The filesystem holding the CLI's configuration cannot lock (some network and FUSE mounts) | Explain it and let the person decide. The fix is a local filesystem for `XDG_CONFIG_HOME`, and moving it hides every profile and stored file credential. After such a move do not reconnect the agent: that rotates its secret. |

A setup the person stops also writes nothing.

### Failed readiness checks

Every run checks every route, including the ones it keeps. A kept route that
fails blocks the whole write, so fix or remove it before other changes can land.

- **Routes: No project is routed.** Every mention would get a holding reply and
  no work. Add a project (step 4).
- **Project `<id>`: the route's directory is no longer usable.** The directory
  was moved or deleted. Ask where the project's work lives now and route it
  again, or remove the route.
- **Project `<id>`: reading the project was refused, and the message says
  Basecamp refuses this read to an Agent identity today.** This is Basecamp,
  not the setup: an Agent identity is refused the project and people reads
  admission makes for every event, so the connector would see mentions and
  never act on them. First check the agent is a member of the project. If it
  is, the way to run today is the **bot-user path**: sign a bot user in under a
  profile of its own (step 1, Bot user) and set that profile up
  (`basecamp connect setup -P '<bot-profile>' --operator-profile '<operator-profile>' --expect-identity <bot-identity-id> --route '<id>=<dir>'`).
  The Agent profile's credential stays as it is. Explain this and let the
  person decide before starting a bot-user sign-in: it needs a bot user account
  and its identity id.
- **Project `<id>`: refused, without the Agent message.** The agent (a bot
  user) cannot see the project. Add it to the project in Basecamp and run setup
  again.
- **Stream ticket: Basecamp refused the ticket mint.** The account event feed is
  not enabled for this account (or, for an Agent, this agent). That is a
  Basecamp-side setting; the person has to ask for it to be enabled.
- **Scope: not full access.** The agent could not reply. For an Agent profile,
  connect again with full access (`basecamp auth agent connect -P '<profile>'`,
  approving full access), with the person's consent since it rotates the
  secret. For a bot user, sign in again with full access and the same pin
  (`basecamp auth login -P '<bot-profile>' --expect-identity <bot-identity-id>`).
  Never switch a bot-user profile to an Agent connection.

Other messages worth knowing: *Operator profile holds no credential* (the
operator signs in with `basecamp auth login -P '<their-profile>'`), *Operator
profile holds an Agent's credential* (pick the person's own profile), *profile
is bound to account X, and this command named account Y* (drop `--account`), and
*Profile holds a person's login, not an Agent's credential* (either it is a bot
user and needs `--expect-identity`, or the wrong login is stored: ask).

## Not built yet

Setup is all there is today. These come with card 24, behind step 21, and do not
exist in the CLI yet, so do not try them or look for flags for them:

- starting and supervising the connector, and reading its pointer lines;
- a `service install` subcommand that keeps it running under systemd or launchd;
- status, doctor and redispatch commands for the connector;
- the Claude Code and Codex plugins that start it.

When the person asks to start the connector, say plainly that setup is done (or
what is left), and that starting it is not available from this skill yet.
