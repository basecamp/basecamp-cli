// Package runner is the connector doing the work itself, with whichever coding
// agent the person chose: `basecamp connect -P <profile> --orchestrator claude
// --worker codex`.
//
// # What it stands in for
//
// The basecamp-connect skill runs in a Claude Code session and leans on two
// things only that harness has: a monitor that wakes the session for each
// request line, and background subagents. The runner supplies both from the
// CLI, so the same skill runs on any coding agent that can do three things:
// take a prompt without a terminal, run shell commands, and continue an
// earlier session.
//
//   - The monitor is the runner's inbox. Each trusted request the connector
//     hands off is kept in the runner's state and delivered to the
//     orchestrator as one turn of a headless session, resumed by its id.
//   - The background subagent is a worker: a headless session of its own, in
//     the repo the orchestrator chose, started by `basecamp connect dispatch`
//     and reported back to the orchestrator as another turn. Unlike a
//     subagent, a worker is kept: a later request on the same card, to-do,
//     message or chat is given to the worker that handled the first, in the
//     same session, and a new one starts only when that session cannot take
//     it.
//
// # What stays where it was
//
// The workflow is the skill's two files, verbatim (instructions.go adds only
// the table that maps the mechanisms they name onto these). Trust, served
// projects, the acknowledgement boost and the ledger are the connector's, and
// nothing here reads a request the connector did not hand off. Everything an
// agent posts goes through the `basecamp` CLI with the agent's profile, as it
// does from the skill.
//
// # Adapters
//
// An Adapter is one coding agent's command line: the arguments of a turn and
// how to read its event stream. Claude Code, Codex and Pi are built in. A turn
// is one process, started through driver.StartWorker so it is the leader of a
// process group the runner can end whole.
//
// # What is durable, and what is not
//
// The runner's state file holds the inbox, the orchestrator's session id and
// every worker. A request is written there before the connector closes its
// record, so a crash between the two repeats the request rather than losing
// it, provided the connector starts again within the handoff's grace of a
// minute: a request older than that at a start is not run late, with or
// without a runner. The repeat is dropped by its event id. A worker that was running
// when the connector stopped is resumed in its own session at the next start
// and told it was interrupted: what it had already done (a post, a commit) is
// the session's to recognize, as it is for the skill after a crash.
//
// A stop ends every agent process it started. A crash ends none, so the state
// names the process of each turn in flight, and a start that finds one named
// ends it, by its process group, before it continues that session: a request
// is not worked on by two processes at once.
//
// Agents run unattended. Under full permissions each adapter starts its CLI
// with permission prompts off; under guarded permissions it starts the CLI
// under that CLI's own guardrails. Who may give the agent work is `basecamp
// connect setup`'s.
package runner
