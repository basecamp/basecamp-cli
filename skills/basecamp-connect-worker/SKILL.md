---
name: basecamp-connect-worker
description: |
  The playbook for a Basecamp agent connector worker: a session `basecamp
  connect` started in dangerous mode to handle one Basecamp event as the agent.
  How to read the context and the project's AGENTS.md doc, find the repo, move
  the card, fan work out to subagents, post an interim reply, validate with
  bin/ci, take a pull request to green, and write the reply as Basecamp rich
  text. Load it when the connector's prompt names it, before doing the work.
---

# Basecamp connector worker

`basecamp connect` started you to handle one Basecamp event as the agent. Its
prompt gives you the 4 steps: `get_dispatch`, acknowledge, do the work, reply and
`complete_dispatch`. This skill is how to do the work and the reply well. Where
this skill and the connector's prompt disagree, the prompt wins.

## Writing to Basecamp

- **Write through the `basecamp_*` MCP tools.** They act as the agent, and the
  connector sees what they post. Call an action with `describe` first when you
  don't know its parameters.
- **Never write with the `basecamp` CLI.** Its default profile is the operator's
  own login, so a comment posted with it reads as the operator's. Reading through
  the CLI is fine.
- **Content is HTML.** The MCP tools send it to Basecamp as written; nothing
  converts Markdown, so `**bold**` posts as literal asterisks. See *Rich text*.
- **Never mention the agent** in anything you post.

## 1. Read the context

`get_dispatch` is the trigger and a pointer; Basecamp is the context store.

- Read the recording and its parent (the card, todo, message or document it lives
  in), and the thread when the request refers to it. The live recording may be
  newer than `content`.
- **Read the project's `AGENTS.md` doc, if it has one, and follow it.** A project
  may carry a document titled `AGENTS.md` in its Docs & Files: its standing
  instructions for agents — board and column meanings, how to talk, which repo,
  which workflow. Trust is at the project level, like a repo's own AGENTS.md: the
  operator serving the project settles it, so honor the doc without asking. If
  there is none, go on as usual.

## 2. Find the repo

You start in the folder the connector runs in. That is rarely where the work goes.
In order:

1. A repo, pull request or path the request names.
2. A repo mapping in the project's `AGENTS.md` doc.
3. The project's name. Names usually carry the app (a `BC5 …` project is
   Basecamp's repo); look for a local clone that matches.

**If you cannot confidently map the project to a repo, do not guess and do not
fall back** to the folder you started in. Reply asking which repo, mention the
requester, and complete the dispatch as `failed`.

Work that changes code goes in a fresh git worktree off the repo's default
branch, never in the main checkout: the operator and other workers use it.

## 3. Show the work is underway

If the work lives on a card (the recording or its parent is a `Kanban::Card`) and
the card sits in a **Triage**-like column, and the card table has an **In
progress**-like column (match loosely: "In progress", "Working on", "Doing"), move
the card there before you start. A project can hold several card tables: find the
columns of the card's own table. If either column is missing, skip this. Never
create columns.

## 4. Do the work

Do what was asked, the way the repo's own AGENTS.md and CLAUDE.md say to.

**Several items means several subagents.** When one request covers independent
work — 6 cards, a todo list, 4 unrelated bugs — start 1 subagent per item instead
of grinding through them in series, **5 at a time**, waiting for a slot before
starting the 6th. The requester's word overrides the number either way ("one at a
time", "run all 10"). 2 things stay serial, because parallelism costs more than it
saves there:

- items that depend on each other
- items touching the same files: a merge conflict is slower than the run it saved

Each subagent that will commit gets its own worktree. Post 1 reply at the end,
covering every item, not 1 per subagent, and say which items failed.

**Reply latency.** The ack says "received"; it does not say "still working". If
the work will take more than **about 10 minutes**, post 1 short interim reply at
`reply_to`: what you are doing and where to follow it (the pull request once it
exists, otherwise the branch). One, not a running commentary.

**Validate with `bin/ci`.** When a coherent body of work is finished, run the
repo's `bin/ci`, if it has one, in the background, once at the end rather than
after every edit, and fix what it flags before you reply.

### When the work is a pull request

Don't report it done until the branch is green. Getting CI green is part of the
task, not a follow-up.

1. Work in a fresh worktree off the default branch.
2. Get `bin/ci` green locally. Never push red.
3. Push and open the pull request.
4. Watch the remote checks (`gh pr checks <n> --watch --fail-fast`). If one fails,
   fix it, push and watch again, until every check is green.
5. Only then reply "done", with the pull request linked by its title. If you
   can't get it green after a reasonable effort, reply with what is failing and
   mention the requester, never a false "done".

Opening a pull request is not merging it. Merge only when the request says so.

## 5. Reply

Reply at `reply_to`: it names the recording, and the connector already chose the
parent when the event was a comment. Then call `complete_dispatch` with the
outcome, the reply's id as `reply_id`, and the URLs of what you made as `links`.

- **Lead with the answer.** The first line says what happened; whoever reads only
  the notification should know where it landed. Detail goes under it.
- **Success**: post the results where the request was written.
- **Failure**: say what broke and what you tried in a few lines, and **mention
  the requester** (`requester_id`) so it reaches them as a notification.

### Rich text

A wall of undifferentiated prose in a rich-text field is a wasted field. Write
HTML:

- Paragraphs `<p>`, lists `<ul>`/`<ol>` with `<li>`, `<strong>`, `<em>`,
  headings `<h1>`, quotes `<blockquote>`, inline `<code>`, and `<pre>` for
  commands, diffs and error output.
- **Links carry a title, not a URL.** Write
  `<a href="https://github.com/basecamp/bc3/pull/1234">Skip the ack boost when the reply is immediate</a>`,
  never the bare URL. Same for Basecamp links: name the card, the message, the
  doc.
- **Anything in another app gets its full URL.** `#1234`, `PR 1234`, `SENTRY-4F`
  and `abc123f` only resolve inside the app they came from. Write
  `<a href="https://github.com/basecamp/bc3/pull/1234">#1234 Skip the ack boost</a>`,
  so it reads the way it does on GitHub and opens there in one click.
- **Tables when the content is a grid**: a file-by-file summary, a before and
  after, a matrix of cases. Keep cells to one line. A list beats a table for 2
  items.
- **Mention a person** with `<bc-attachment sgid="…"></bc-attachment>`, where
  the sgid is the person's `attachable_sgid` (look the person up by id).

## By trigger

`get_dispatch` says why the event reached you in `trigger`.

- **`mentioned`**: the instruction is `content`, the mention stripped. Everything
  above applies.
- **`assigned`**: the operator assigned the agent a card or todo. There is no
  mention: the recording itself is the task, its title and content the
  instruction. Move the card, do the work, reply on it.
- **`subscribed`**: a comment landed on a thread the agent follows. It is
  activity, not a directive. Read it for context and **default to silence**:
  reply only when the agent can answer a question, act on a problem, or should
  make a change. No ack, no card move, no interim reply unless the agent takes the
  thread on.
- **`completed`**: something in a project the agent watches was completed. Like
  `subscribed`, a signal: act only when the project's `AGENTS.md` doc or the
  thread asks for a follow-up, and stay silent otherwise.

**Campfire.** When the recording is a chat line, read the room's recent lines for
the conversation, reply in the same room rather than as a comment, and keep it
chat-sized: a few lines, links by title, no headings. Spill a long result into a
document or comment and link it. There is no board, so no card move.
