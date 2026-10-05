---
name: basecamp-connect
description: |
  Drive your own Claude Code session from Basecamp. Starts the local connector
  (basecamp connect), which listens to the account's event feed as a Basecamp
  agent and prints one line per trusted request: a mention, an assignment, a
  comment on a thread the agent follows. This session acknowledges each one
  within seconds (a boost in its own words), picks the repo for the project,
  and hands the request to a background subagent that reads the context, does
  the work with your settings and tools, and replies as the agent. The session
  stays free to take the next request, and you can keep using it.
  Also connects an agent to this computer and manages its setup: the agent's
  credential (basecamp auth agent connect), who may give it work, and which
  projects it serves (basecamp connect setup), plus status and doctor.
  Use when asked to connect an agent, drive agents from Basecamp, watch
  Basecamp for an agent's mentions, start or stop the connector, or change
  who can give the agent work or which projects it serves.
triggers:
  - /basecamp-connect
  - connect an agent
  - drive agents from basecamp
  - watch basecamp for agent commands
  - start the connector
  - stop the connector
  - is the connector running
  - set up the connector
  - basecamp connect setup
  - basecamp auth agent connect
  - serve a project
  - who can give the agent work
  - connector not ready
  - basecamp connect status
  - basecamp connect doctor
---

# /basecamp-connect: drive your agent from Basecamp

Someone @mentions the agent in Basecamp (or assigns it a card or to-do). This
session acknowledges it as the agent within seconds, a background subagent
does the work in the right repo with your own Claude Code setup, and the agent
replies in place.

Three pieces:

- **The agent**: a Basecamp person of its own, owned by you, connected to this
  computer under a CLI profile (often named after the agent).
- **The connector**: `basecamp connect -P '<profile>'`. It listens to the
  account's event feed as the agent, checks each event against who may give the
  agent work and which projects it serves, and prints one line per trusted
  request. It does nothing else: no workers, no acknowledgements, no replies.
- **This session**: the orchestrator. It watches those lines, acknowledges each
  request, chooses the repo, and dispatches one background subagent per
  request. It never does the work itself.

Claude Code only. The connector runs on Linux and macOS.

## Invocation

**The arguments are natural language, not a grammar.** Pull out the agent, the
projects, and any wishes about who may give it work, and translate them into
the commands below. Never make the person restate them as flags.

```
/basecamp-connect                                        # the last agent used, confirmed first
/basecamp-connect as Marie                               # start the connector for that agent
/basecamp-connect as Marie, and let anyone in "Launch" give it work
/basecamp-connect connect my new agent and serve the Redesign project
```

Each becomes a flag on setup (*First-time setup*, step 4):

| They say | Setup flag |
|---|---|
| "only me" (the default) | `--trust operator` |
| "me and Jane" | `--allow <jane's person id>` (and everyone else who stays: the list is replaced) |
| "anyone in the project" | `--trust project` |
| "also work in project X" | `--serve <id of X>` |
| "stop working in X" | `--unserve <id of X>` |

Then:

1. **Which agent.** Use the profile they name. With none, read
   `~/.config/basecamp-connect/last.json` (below) and confirm it. With neither,
   list profiles (`basecamp profile list --json`) and ask.
2. **Set up, if needed.** `basecamp connect show -P '<profile>' --json`. A
   `not_found` or `unknown profile` error means it isn't set up: follow
   *First-time setup*. If they asked for a change, run setup with it.
3. **Start the connector** (*Running the connector*) and watch it.
4. **Handle each request** (*For each request*), until they ask you to stop.

Trust and served projects are read when the connector starts. After any setup
change while it runs, stop it and start it again.

### Remembered settings

After every successful start, write `~/.config/basecamp-connect/last.json`
(create the directory if needed):

```json
{
  "profile": "marie",
  "repos": { "48699913": { "name": "Bring your agents to Basecamp", "path": "/home/me/code/agents" } },
  "saved_at": "2026-10-01T15:00:00Z"
}
```

`repos` maps a project's id to the local repo its work goes in, once the
person has confirmed it (see *Choose the repo*). A start that fails must not
overwrite the file. Invoked with no arguments, show the stored profile and
mappings and ask before starting. Never start silently from the store.

## Rules without exceptions

**Credentials**

- Never ask the person to paste a token, secret or password into the
  conversation, and never put one in a flag, an environment variable or a file.
- Never print, read or copy a stored credential or the CLI's credential files.
- Credentials enter only through `basecamp auth agent connect`, or on the
  bot-user path `basecamp profile create` / `basecamp auth login` with
  `--expect-identity`. If a hint suggests `--with-token` or
  `--with-client-credentials`, don't follow it.
- The link and one-time code a connection prints are for the person at this
  computer. Show them here only, never in Basecamp or a file.
- Setup and the connector refuse to run while `BASECAMP_TOKEN` is set. Ask the
  person to unset it; never work around it.

**Identity.** Never set up a profile whose identity the person hasn't
confirmed. Before the first setup, run `basecamp me -P '<profile>' --json` and
say who it is: `identity` (name, email) and, when present, `person.name` and
`person.id`. Go on only when they say that is the agent. If it names someone
else, stop: don't run setup and don't reconnect.

**Writing to Basecamp.** Everything this skill and its subagents post (boosts,
comments, chat lines, card moves) goes out **as the agent**: always pass
`--profile '<profile>'`. Without it, the CLI uses the default profile, which is
usually your own login, and the post reads as yours. Never mention the agent in
anything you post.

**Shell quoting.**

- **Numeric ids** go in bare, digits only, and only ids you got from the CLI's
  own output or a request line.
- **Every other value** goes in single quotes: profile names, anything the
  person typed. Write a single quote inside a value as `'\''`.
- Project names never reach a command. Resolve each name to its numeric id
  first (`basecamp projects list -P '<profile>' --json`), and pass the id. The
  project called `Launch $(date)` is served as `--serve 222`, never by name.

**Interactive logins.** `basecamp auth agent connect`, `basecamp auth login` and
`basecamp profile create` print a link and wait for a person. Run them without
`--json` or `--quiet`, in the background, and read their output as it arrives,
so you can show the link and code while they wait.

## First-time setup

Ask only what you can't find out.

**1. Profile and credential.** Agree a profile name (letters, digits, `-` and
`_`), for example the agent's name. Check it: `basecamp auth status -P
'<profile>' --json`.

- `unknown profile`: it doesn't exist. Connect the agent with
  `basecamp auth agent connect -P '<profile>'` (add `--no-browser` if the person
  is on another device). Show the link and code. They open the link, check the
  code, choose the agent (or create one) and approve.
- `oauth_type` `agent`: already connected. Don't connect again: that rotates
  the agent's secret and disconnects any other computer using it.
- Anything else: a person's login is stored. Ask; never connect an agent over
  it.

Then confirm the identity (`basecamp me`, see *Identity*).

**Bot user** (a regular Basecamp user acting as the agent): sign in as the bot,
pinned to its identity id, with
`basecamp profile create '<bot-profile>' --account <account-id> --expect-identity <bot-identity-id>`
(or `basecamp auth login -P '<bot-profile>' --expect-identity <bot-identity-id>`
for an existing profile), and pass the same `--expect-identity` to the first
setup.

**2. Who may give it work.** Explain in a sentence each, and default to the
first:

- **operator**: only the operator, which for a personal agent is its owner (you).
- **allowlist**: the operator plus people you name.
- **project**: the operator plus anyone in the project who isn't a client.

Assignments count only from the operator, in every mode. For a personal agent
pass no operator flag: setup takes its owner. For any other agent, name the
operator by their own CLI profile with `--operator-profile '<profile>'`. For
allowlist, look up each person's id (`basecamp people list --json`) and pass
`--allow <id>` for each.

**3. Projects, by name.** List them (`basecamp projects list -P '<profile>'
--json`; if that's refused under an Agent identity, list them with the person's
own profile instead). Show the names, let them choose, and map each to its id
yourself. When a name matches more than one project, ask. The agent must be a
member of each project it serves.

**4. Confirm, then run setup.** Say back the agent, who may give it work, and
each project by name. Then:

```bash
basecamp connect setup -P '<profile>' --serve <project-id> --serve <project-id> --json
```

adding `--trust`, `--allow` or `--operator-profile` as chosen, and on the
bot-user path `--expect-identity <bot-identity-id>`.

### Reading setup's result

Success is `{"ok": true, "data": {...}}` with `data.ready` and `data.written`
true and a list of `checks`. A `warn` check is usable; mention it. After the
first setup, check `data.agent_person_id` matches the `person.id` that `me`
reported.

A failure is `{"ok": false, "error", "code", "hint"}` and **nothing was
written**. Explain the `error` in plain words, and follow the `hint` only when
these rules allow it.

| `code` | Means | Next |
|---|---|---|
| `usage` | A bad value, or a person trust refuses (an agent, a client) | Fix what the message names |
| `auth_required` | No usable credential, or not the agent connect.json names | Connect it (step 1), or confirm which agent this profile should be |
| `api_error` | Usually `unknown profile` | Connect the agent first |
| `not_ready` | A readiness check failed; `error` lists each | Explain each (below) |
| `busy` | Another setup holds the profile | Run it again in a moment |

Failed checks worth knowing:

- **Project `<id>`: reading the project was refused, and Basecamp refuses this
  read to an Agent identity today.** First check the agent is a member. If it
  is, the way to run today is the bot-user path above. Explain, and let the
  person decide.
- **Project `<id>`: refused, without that message.** The agent can't see the
  project. Add it to the project in Basecamp and run setup again.
- **Stream ticket refused.** The account event feed isn't enabled for this
  account or agent. That's a Basecamp setting the person has to ask for.
- **Scope: not full access.** The agent couldn't reply. Connect again with full
  access, with the person's consent, since it rotates the secret.

**Never edit connect.json by hand**, and never read it directly: use
`basecamp connect show -P '<profile>' --json`, which checks the file is safe
first. Every change goes through setup, which keeps whatever you don't pass.

## Running the connector

### 1. Start it, and watch its output

Only one connector per agent can run at a time. If it's already running
elsewhere, it refuses to start and says so: tell the person, and don't start a
second.

Start it in the background with the Bash tool (`run_in_background: true`), and
note the output file the harness reports:

```bash
basecamp connect -P '<profile>'
```

It runs until stopped. Its folder doesn't matter: every subagent works in the
repo you choose for it. Read the output once after a few seconds. You should
see `connector: running` in its log. If it exited instead, explain the error
(see *When the connector stops*), and stop. If it says `could not get the
agent's token yet`, Basecamp's token endpoint is rate-limiting it or having
trouble: it waits as long as it says and tries again on its own. Tell the
person, leave it running, and arm the monitor as usual. Restarting it doesn't
help.

Then arm a persistent monitor on that file with the Monitor tool
(`persistent: true`), replaying from the start so nothing that arrived between
the start and the monitor is missed:

```bash
tail -f -n +1 '<connector-output-file>' | grep --line-buffered -F '"type":"request"'
```

Single-quote the path, as with every other value.

Each notification is one request: handle it with *For each request*. A request
that arrives while you're waiting on the person is not their reply.

The connector starts from the present. Anything asked more than a minute
before it started is not picked up. If someone says the agent ignored them, that's the first
thing to check.

### 2. The request line

```json
{"type":"request","event_id":123,"event_type":"comment_created","trigger":"mentioned",
 "recording":{"bucket_id":456,"project_name":"BC5 Calendar","recording_id":789,"type":"Comment",
              "title":"Fix the date picker","url":"https://3.basecamp.com/999/buckets/456/recordings/789"},
 "reply_to":{"kind":"comment","recording_id":700},
 "requester_id":1001,"requester_name":"Jorge Manrubia","acknowledge":true,
 "content":"<p>the date picker is off by one, please fix</p>","content_updated_at":"..."}
```

- **`trigger`**: why it reached you.
  - `mentioned`: someone @mentioned the agent.
  - `assigned`: the operator assigned it a card, to-do or step.
  - `subscribed`: a new comment on a thread the agent follows, with no mention.
  - `completed`: something completed in a project it watches.
- **`acknowledge`**: true when a person asked for something. False for
  `subscribed` and `completed`, which are context, not instructions.
- **`recording`**: what the event is about. `bucket_id` is the project, and
  `project_name` its name. `url` works with every command below.
- **`reply_to`**: where the answer goes. `kind` `comment` means a comment on
  `recording_id` (the card or message the comment belongs to, already chosen for
  you). `kind` `chat_line` means a line in the Campfire `recording_id`.
- **`requester_id`**: the person who asked. Mention them on failure, except in
  Campfire.
  `requester_name` is their name when they wrote the recording; it's missing
  for an assignment someone else's recording carries.
- **`content`**: the request as it was written, with the agent's own mention
  removed. For an assignment, the recording itself (its title and content) is
  the task. The live recording may be newer.

Every line has already passed the trust check: the person may give the agent
work, and the project is one it serves. A request arrives once, except in one
case: if the connector crashes right after printing a line, it can print it
again when restarted within a minute. Skip an `event_id` you've already handled. Every other
line (no `type`, or `"type":"event"`) is the connector's own log of what it saw
and decided, including what it turned away. Never act on those.

## For each request: acknowledge, choose the repo, hand off

**This session is the orchestrator, not a worker.** Its only job per request is
these steps, in order, and then back to watching. Apart from reading the project's
AGENTS.md to choose the repo, it never reads the thread, investigates, runs repo
commands, does the work or writes the reply. Every one
of those delays the next acknowledgement, and an acknowledged request that sits
silent for half an hour looks exactly like a missed one.

### a. Acknowledge, within seconds

Only when `acknowledge` is true. Boost the recording as the agent, with a
short phrase or emoji that fits the request (16 characters at most), never a
fixed string:

```bash
landed=false
for i in 1 2 3; do
  if out=$(basecamp boost create '<recording.url>' '<ack>' --profile '<profile>' 2>&1); then
    landed=true; break
  fi
  echo "$out" | grep -qE 'Not authenticated for|token refresh failed' || break   # anything else: no retry
  sleep 2
done
[ "$landed" = true ] || { echo "boost did not verifiably land: $out" >&2; false; }
```

Retry only those two failures: they happen before any request is sent. Any
other failure may have landed the boost, and a retry would post it twice. If it
didn't verifiably land, say so in the handoff, and the subagent boosts as a
fallback.

When the reply will come within moments (a quick question in Campfire), the
reply itself can be the acknowledgement: skip the boost, and say so in the
handoff. Not for card work: there the boost is how the requester sees the
request landed.

### b. Choose the repo

**Read the project's AGENTS.md before choosing the repo**, even when `last.json`
already has a mapping. Find a document titled `AGENTS.md` in the project's Docs
& Files. Use the project's recording index: it includes documents in nested
folders, unlike a root-vault document listing. Fetch every page:

```bash
basecamp recordings documents --all --project <bucket_id> --json -P '<profile>'
basecamp docs show <doc-id> --project <bucket_id> --json -P '<profile>'
```

If it declares a repo mapping for this work, that takes precedence over the
remembered mapping, the request's repo hints, and the project-name heuristic.
Pass the document and its mapping to the subagent, or explicitly pass
"AGENTS.md checked: none found" after a successful search with no match.
If more than one document has that title, ask which governs rather than choosing
one arbitrarily. If no such document exists,
or it declares no applicable mapping, work out the repo from:

1. `repos` in `last.json`, if this project is there.
2. A repo the request itself names (a pull request, a repo, a path).
3. The project's name, `recording.project_name`: names usually carry the app
   (a `BC5 …` project is Basecamp's repo). Look for a matching clone under the
   person's usual code folders.

A failed document read isn't an absent document. If the read fails, or its repo
mapping is ambiguous or can't be found locally, use the holding reply and ask
the person below rather than silently falling back. Keep remembered mappings
operator-confirmed: ask before replacing one that disagrees with the document.

**If you can't map it confidently, ask the person. Don't guess, and don't fall
back to this folder.** Before asking, post one short holding reply as the agent
at `reply_to`, mentioning the requester (never in Campfire, where Basecamp
refuses an agent's mention): received, waiting for the operator to
pick a repo. Never leave an acknowledged request with nobody holding it. Once
they answer, store the mapping in `last.json`.

Requests that need no repo (a question about the project, a summary, Basecamp
work only) go to a subagent with no repo.

### c. Dispatch one background subagent

Use the Agent tool with `run_in_background: true`. Give it everything it needs
to finish without this session:

- the whole request line;
- the agent's profile name, and the repo path (or "no repo"). A subagent
  doesn't start in that repo by itself: say plainly that it must work there;
- whether an acknowledgement is still owed (the boost failed), or not owed
  (it landed, or the reply is the acknowledgement);
- the project's AGENTS.md document and any repo mapping used, or the explicit
  result "AGENTS.md checked: none found";
- the subagent instructions below, in full.

Then go straight back to watching. There's no limit on requests in flight.

Keep the terminal quiet. The reply in Basecamp is the record: don't recap
routine requests here. Speak up only for a failure, a refusal, or something
the person must decide (a repo to pick, a mention that couldn't be posted).

## The subagent's instructions

Give each subagent this section.

You handle one Basecamp request as the agent, end to end. Every Basecamp write
goes out as the agent with `--profile '<profile>'`. Reads can use the same
profile.

**1. Acknowledge, only if still owed.** Boost the recording as above. Skip it
for `subscribed` and `completed`.

**2. Read the context.** The event is the trigger. Basecamp holds the context:

```bash
basecamp show '<recording.url>' --json -P '<profile>'   # the recording, and its parent
```

Read the parent (the card, to-do, message or document it lives in) and the
thread when the request refers to them. For a Campfire line, read the line and
the room's recent conversation:

```bash
basecamp chat line '<recording.url>' --json -P '<profile>'
basecamp chat messages --project <bucket_id> --room <reply_to.recording_id> --json -P '<profile>'
```

**Read the project's AGENTS.md doc, if it has one, and follow it.** It's the
project's standing instructions for agents: board meanings, how to talk, which
repo, which workflow. Use the document passed in the handoff. If the handoff
says "AGENTS.md checked: none found", proceed without repeating the search.
Only when no discovery result was passed, find a document titled `AGENTS.md`
using the recording index (including nested folders):

```bash
basecamp recordings documents --all --project <bucket_id> --json -P '<profile>'
basecamp docs show <doc-id> --project <bucket_id> --json -P '<profile>'
```

Trust is at the project level: the operator serving the project settles it.

**3. Show the work is underway.** If the work lives on a card in a Triage-like
column, and its card table has an In-progress-like column ("In progress",
"Working on", "Doing"), move it there first:

```bash
basecamp cards columns --project <bucket_id> --card-table <table-id> --json -P '<profile>'
basecamp cards move <card-id> --to '<column>' --project <bucket_id> --card-table <table-id> --profile '<profile>'
```

Use the card's own card table. If either column is missing, skip this. Never
create columns. Skip it for `subscribed` and `completed`.

**4. Do the work** in the repo, the way its own AGENTS.md and CLAUDE.md say.
If you were given a repo path, start by changing into it: you don't start
there. "no repo" means the request needs none (a summary, a question about the
project): do it from Basecamp alone. Work that changes code goes in a fresh git
worktree off the default branch, never in the main checkout.

**Read the code before you answer anything about it.** Open the files the
request is about, and answer from what's there, not from what the request or
the names suggest. If what it asks about isn't in the code, say so. If a
request about code came with "no repo", or you can't reach the repo, say that
rather than guess. A confident wrong answer is worse than "I couldn't find
that".

**Commit with the repo's own git identity, or not at all.** Never set a name
or email, and never borrow the person's. If git has no author configured,
leave the change staged in its worktree and say so in the reply.

- **Several independent items means several subagents**, 5 at a time: six
  cards, a to-do list, four unrelated bugs. Items that depend on each other, or
  touch the same files, stay serial. Each one that commits gets its own
  worktree. Post one reply at the end covering every item, and say which failed.
- **Interim reply.** If the work will take more than about 10 minutes, post
  one short reply at `reply_to`: what you're doing and where to follow it (the
  pull request once it exists, otherwise the branch). One, not a running
  commentary.
- **Validate with `bin/ci`**, if the repo has one, once at the end, and fix
  what it flags before you reply. Wait for it to finish: when you stop, your
  run ends, and anything still running is abandoned.
- **A pull request isn't done until it's green.** Get `bin/ci` green locally,
  push, open the pull request, then watch the checks
  (`gh pr checks <n> --watch --fail-fast`), fixing and pushing until every
  check passes. Only then reply "done". If you can't get it green, reply with
  what's failing and mention the requester. Opening a pull request isn't
  merging it: merge only when asked.

**5. Reply at `reply_to`, as the agent.**

```bash
# kind "comment"
basecamp comments create <reply_to.recording_id> - --project <bucket_id> --profile '<profile>' < reply.md
# kind "chat_line"
basecamp chat post - --project <bucket_id> --room <reply_to.recording_id> --profile '<profile>' < reply.md
```

- **Post exactly once.** Never repost to fix formatting, or because you're
  unsure it landed: read the thread or room to check instead. Basecamp
  doesn't let agents delete what they post, so a duplicate stays until a
  person removes it.
- **Lead with the answer.** The first line says what happened. Detail goes
  under it.
- **Success**: the results, where the request was written.
- **Failure**: what broke and what you tried, in a few lines, and **mention the
  requester**: `[@<requester_name>](person:<requester_id>)`. Without a name,
  look it up with the person's own login (`basecamp people show <requester_id>
  --json`, no `-P`).
  Basecamp may refuse an agent's comment that mentions someone ("Basecamp
  doesn't let agents do this"). If it does, post the same reply without the
  mention, and tell the main session so it can tell the person directly. The
  same goes for every reply that mentions someone, the main session's holding
  reply included.
  In Campfire (`kind` `chat_line`), never mention anyone, since Basecamp
  refuses it there: post the reply without the mention, and tell the main
  session so it can tell the person directly.
- Never mention the agent.

**Rich text.** In comments, messages, documents and cards, the CLI converts
Markdown: headings, **bold**, lists, quotes, fenced code for commands, diffs
and errors, and pipe tables for real grids. Don't hand-write HTML there: raw
tags switch off the conversion for the whole field.

Campfire is different: `chat post` sends plain text and leaves Markdown as
typed. The exception is a line with an `@`: the CLI reads it as a mention and
converts the line to HTML, and Basecamp refuses an agent's chat post that
carries a mention. So keep `@` out of the agent's chat lines, and write them as
plain text, or as real HTML with `--content-type text/html`. For any other CLI
detail, load the `basecamp` skill.

- **Links carry a title, not a bare URL**:
  `[Skip the ack boost when the reply is immediate](https://github.com/basecamp/bc3/pull/1234)`.
  In a plain-text Campfire line, where Markdown isn't converted, write the
  title and then the full URL: `Skip the ack boost: https://github.com/…/pull/1234`.
- **Anything in another app gets its full URL**: `[#1234 Skip the ack boost](https://github.com/…/pull/1234)`,
  never a bare `#1234`, `abc123f` or `SENTRY-4F`.
- **Campfire replies stay chat-sized**: a few lines of plain text (or HTML), no
  headings. Spill a long result into a comment or document and link it.

**By trigger:**

- **`mentioned`**: the instruction is `content`. Everything above applies.
- **`assigned`**: the recording is the task. Move the card, do the work, reply
  on it.
- **`subscribed`**: activity on a thread the agent follows, not an instruction.
  Read it and **default to silence**. Reply only to answer a question, act on a
  problem, or make a change the thread needs. No boost, no card move, no interim
  reply unless the agent takes the thread on.
- **`completed`**: a signal, like `subscribed`. Act only when the project's
  AGENTS.md or the thread asks for a follow-up.

**Transient CLI failures.** With several subagents at once, the CLI
occasionally fails with `Not authenticated for profile:` or `token refresh
failed:`. That's the credential store under concurrent use, not a missing
login. Retry 2 or 3 times with a short pause, and only on those two messages.
Never run `basecamp auth login` in response, and never report the profile as
missing.

**6. Report back in one line**: posted or failed, the line or comment id,
and any refusal the main session must pass on. Don't repeat the reply: the
person reads it in Basecamp.

## When the connector stops

The background task notifies you when the connector exits. Read the end of its
output and tell the person what it means:

- **"was disconnected in Basecamp, or connected on another computer"**: run
  `basecamp connect setup -P '<profile>'` (it reconnects with one approval),
  then start it again.
- **Already running**: another connector for this agent holds its lock, on
  this computer or in another session. Don't start a second.
- **`BASECAMP_TOKEN` is set**: the person unsets it, then you start it again.
- **Anything else**: show the error, and run `basecamp connect doctor -P
  '<profile>'`.

Requests that arrive while it's stopped are not picked up later.

## Checking on it

- `basecamp connect doctor -P '<profile>'`: the credential, the identity, the
  event feed and the ledger. Start here when something seems wrong. It posts
  nothing.
- **"Did that mention reach it?"**: search the connector's output file for the
  recording id. A `"type":"request"` line means it was handed to you. A
  `"type":"event"` line with a `reason` says why it was turned away (for
  example `untrusted_performer`). No line means the feed never delivered it.
- `basecamp connect status -P '<profile>'`: the feed's position, holds and
  losses. Read-only, and safe while the connector runs. Handed-off requests
  don't appear here; the output file is the record of those.

## Stopping

When the person asks you to stop, or the session is ending, stop the connector
task (TaskStop, or send it SIGINT). There's nothing else to clean up: no
webhooks, nothing public. Subagents already running finish their requests and
reply. Say that new mentions won't be picked up until it's started again.
