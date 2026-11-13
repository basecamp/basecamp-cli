# Basecamp CLI Development Context

## Getting Started

See [CONTRIBUTING.md](CONTRIBUTING.md) for build setup, testing, and PR workflow.

The standard development loop in this repo: make changes, run `bin/ci`, fix
what it catches, repeat until green, then push. Treat `bin/ci` as your
inner-loop companion, not a final hurdle.

## Repository Structure

```
basecamp-cli/
├── cmd/basecamp/     # Main entrypoint
├── internal/
│   ├── appctx/       # Application context
│   ├── auth/         # OAuth authentication
│   ├── cli/          # CLI framework
│   ├── commands/     # Command implementations
│   ├── completion/   # Shell completion
│   ├── config/       # Configuration management
│   ├── dateparse/    # Date parsing
│   ├── hostutil/     # Host utilities
│   ├── models/       # Data models
│   ├── names/        # Name resolution
│   ├── observability/ # Tracing and metrics
│   ├── output/       # Output formatting
│   ├── presenter/    # Output presentation
│   ├── resilience/   # Retry and backoff
│   ├── sdk/          # Basecamp SDK wrapper
│   ├── tui/          # Terminal UI
│   └── version/      # Version info
├── e2e/              # BATS integration tests
├── skills/           # Agent skills
├── hooks/            # Agent lifecycle hooks (both plugin agents)
├── .claude-plugin/   # Claude Code integration
└── .codex-plugin/    # Codex plugin manifest
```

## Coding-agent integrations

Coding-agent integration lives in `internal/harness` (agent registry, detection, plugin and
skill health checks) and `internal/commands/wizard_agents.go` (`basecamp setup
claude|codex|grok|agents`). Claude Code and Codex each get a native plugin from the
`basecamp/claude-plugins` marketplace and have registrations of their own (`claude.go`,
`codex.go`). The plugin is `basecamp-cli@37signals`. During the deprecation window the marketplace keeps
`basecamp` as an alias with the same source; setup treats `basecamp@37signals` as stale and
replaces it by key, and the SessionStart hook (routed through `agent-hook pre-commit-snapshot`
so older CLIs stay silent) tells alias installs once to switch. That is safe only while
`basecamp` in the marketplace means this plugin: drop the `isStalePluginKey` /
`CodexLegacyPluginKey` handling before the marketplace points `basecamp` at the hosted
connector. Grok Build has no plugin: it reads the shared `~/.agents/skills/basecamp` skill
directly, so it is a row of `harness.SkillAgent` (name, id, home env var, home directory,
binary) in `skill_agent.go`, and everything in `internal/commands` that touches a shared-skill
agent — the setup handler, the `BASECAMP_SETUP_AGENT` values, doctor's remediation — loops over
`harness.SkillAgents()` rather than naming it. A new shared-skill agent is a new row; the skill
picker's `(Global)` row, the prose lists in the installers and the docs are the places to update
by hand. `setup <id>` never fabricates an agent's home directory: a skill-only agent that is not
detected is reported missing, not created.

## The connector doing the work itself

`basecamp connect -P <profile>` prints each trusted request for the session that started it,
which is how the basecamp-connect skill runs in Claude Code. With `--orchestrator <agent>` the
connector does that session's job itself, on any coding agent that can take a prompt without a
terminal, run shell commands and continue an earlier session. `internal/connector/runner` is
all of it:

- **The workflow is the skill.** `skills/basecamp-connect/SKILL.md` and `subagent.md` are
  embedded (`skills.Connect`) and given to the agents as they are. `instructions.go` adds only
  the table that maps the Claude Code mechanisms they name (the monitor, background subagents)
  onto the runner's commands. Change behavior in the skill, never there.
- **An adapter is one CLI**: the arguments of a headless turn and how its event stream reads
  (`adapter.go`: `claude -p`, `codex exec`, `pi -p`). A new coding agent is a new adapter and a
  stream fixture in `adapter_test.go`. `go test -tags runner_live ./internal/connector/runner
  -run TestInstalled` holds each adapter to the installed CLI and spends a few model requests.
- **The orchestrator's tools are CLI commands** that reach the running connector over a unix
  socket in its state directory: `basecamp connect dispatch`, `workers list|send|stop`, `say`,
  `stop`, `restart` (`internal/commands/connect_runner.go`).
- **Which agent does what**: the start's flags, then `runner.json` beside the profile's
  `connect.json` (hand-written, read again for every dispatch), whose rules choose a worker by
  repo, project, card table, to-do list, any recording a request sits under, or a Ping.
- **A worker is kept for its thread.** A later request on the same card, to-do, message or
  chat goes to the worker that handled the first, continued in its session, and waits its turn
  if that worker is busy. A new worker starts only when the session cannot take it: the work
  is in another repo or on another coding agent, `--again` was given, or the CLI no longer
  has the session. The orchestrator's own session is never rotated; each CLI's compaction is
  relied on, and both kinds of session are told where their instructions are kept.
- **State** is `runner/state.json` in the connector's state directory: the orchestrator's
  inbox and session, and every worker. The handoff gives the runner each request before it
  closes the record (`HandoffOptions.Deliver`), so a crash repeats a request rather than
  losing it if the connector starts again within `HandoffGrace` (a minute), and a worker a
  stop interrupted is continued in its own session at the next start. A start ends what a
  crash left running first, and does not start beside a process it cannot confirm ended; a
  turn's process is recorded before its agent is told anything, and a session is not given
  another turn while what its last one started cannot be confirmed ended.

**What agents may do** is one switch, `--permissions`, kept in `runner.json` and read for
every turn. `full`, the default, starts each CLI with its permission prompts off: a headless
turn has nobody to answer one. `guarded` starts each under its own unattended guardrails
(`Adapter.Guarded`): Claude Code in its auto mode, and Codex in its workspace-write sandbox
with the network on and its automatic review, which lets Codex's own reviewer answer a
request to run a stopped command outside the sandbox. Without that review a sandboxed Codex
can neither commit in a repo (its git data is read-only there) nor read `connect.json`
(inside the sandbox `/` belongs to nobody the CLI knows, so the check that the file is the
person's own fails). That check is the trust anchor's and is not loosened: instead an agent
is told which runner started it (`runner.SocketEnv`), so `dispatch` and the other runner commands
reach the socket without reading the file. Pi has no guardrails and is refused under
`guarded`. What a mode means is told to the worker (`guardedNote`), the person at start
(`PermissionsNotice`) and in the skill; keep the three in step. A `runner.json` that cannot
be read starts no agent: it is never read as `full`.

**What a start names is kept** (`SaveStart`): the orchestrator, the worker, the
permissions and a cap on workers go into `runner.json`, and `--run` starts with them.
Plain `basecamp connect` never reads them, so the Claude Code skill's path is untouched.

**How many workers run at once** has no cap unless a start names one with
`--max-workers`: every request gets a worker as it arrives. A cap is `max_workers` in
`runner.json`, and `--max-workers 0` takes a kept one off. Unlike the rules and the
permissions, which are read again as the connector runs, a cap is read once, at a start.

## Basecamp API Reference

API documentation: https://github.com/basecamp/bc3-api

Key endpoints used by the CLI:
- `/projects.json` - List projects
- `/buckets/{id}/todolists/{id}/todos.json` - Todos in a list
- `/buckets/{id}/todos/{id}/completion.json` - Complete todo
- `/people.json` - List people
- `/my/profile.json` - Current user

**Search:** Use `basecamp search "query"` for full-text search across projects. The Recordings API (`basecamp recordings`) is for browsing by type/status without a search term.

## Testing

`bin/ci` is the local CI gate. It runs every check that remote CI runs:
formatting, vetting, linting, unit tests, e2e tests, naming conventions,
CLI surface snapshots, skill drift detection, SDK provenance, and go mod tidy.
Run it early and often — after finishing a feature, after fixing a bug, before
pushing. If you're about to `git push` and haven't run `bin/ci` in this
session, stop and run it first.

**Skill drift**: `make check-skill-drift` runs over `skills/basecamp/SKILL.md`,
`skills/basecamp-doctor/SKILL.md`, `skills/basecamp-connect/SKILL.md` and its
`skills/basecamp-connect/subagent.md`, checking that the commands and flags each one
*references* still exist in the `.surface` snapshot. It catches stale references, not
missing coverage, so adding a command breaks none of them.

A reference has to resolve exactly: `basecamp setup <removed>` no longer passes by
falling back to `basecamp setup`. Words past a resolved command are allowed only where
they cannot name a subcommand — after a command that takes positional arguments, and
after a leaf that has none — so argument values and prose still pass; a word that runs
on into a filename or key (`upload report.pdf`, `config set project_id`) is read as the
argument it is. A word naming a command in `.surface-breaking`, the record of removals,
fails before either escape can take it, which is how removal is caught. Two limits
remain. Under a command that has both arguments and subcommands (`basecamp recordings`),
a subcommand that never existed passes as an argument value. And `.surface` cannot say
whether a group also runs bare (`basecamp skill` prints the skill file), so a word after
one fails even where the CLI would accept it; hidden commands are not in `.surface` at
all. Acknowledge a deliberate case with the baseline entry its DRIFT line names, in
`.surface-skill-drift`. `make test-skill-drift` runs the check against fixtures that hold
all of this.

Update the skill the change actually affects;
basecamp-doctor deliberately covers only doctor, setup and auth remediation;
basecamp-connect covers `basecamp auth agent connect` and `basecamp connect`, and its
evals run against its own SKILL.md (`make -C skill-evals eval-connect`).

**Hint commands**: a hint is an instruction an operator is about to run, so
`TestHintCommandsResolve` holds the commands hints name to the same exactness, against
the real command tree rather than `.surface`. It reads hint text in `internal/commands`
and `internal/connector/setup` — hints written inline, built into a variable, returned
by a helper, passed through a wrapper, or assigned to a hint-named field — and every
string in `connect.go`. Asking
the command lets it be exact where the script cannot: a word after a group passes only
if the group is not the root, runs, and its own argument validator accepts the word — which a subcommand
that never existed can still satisfy, under a group like `recordings` that takes one. It
checks commands, not flags.

```bash
bin/ci                # The single command — run this
```

When iterating on a specific area, use targeted make targets for faster
feedback, then finish with `bin/ci` before pushing:

```bash
make build            # Build binary to ./bin/basecamp
make test             # Go unit tests
make test-e2e         # BATS end-to-end tests
make lint             # Linter
make check            # All checks (what bin/ci runs)
```

**Dead code**: `make deadcode` reports unreachable functions whole-program, rooted
at the shipped binary and again with `-tags dev`. It is a report, not a gate, and
is deliberately outside `make check`. The linter's `unused` cannot do this job —
it runs per-package and counts every exported identifier in a non-main package as
used, which is how 600 lines of exported, zero-caller `internal/tui` API survived
it. Root it at the binary, not `./...`: only main packages are roots, so `./...`
reports everything no main reaches — mostly the `dev`-tagged workspace tree — as
unreachable.

It also analyzes one GOOS/GOARCH at a time, while we release five. A host run says
nothing about the others: code behind another platform's build tag is never loaded,
and a function whose only caller sits behind one looks unreachable. Before deleting
anything platform-adjacent, check the other targets — install the tool for the host
and set GOOS for the analysis (`GOOS=windows deadcode ./cmd/basecamp`), since
`GOOS=windows go run` cross-compiles the tool itself and fails.

Read the output before acting on it: a zero-caller exported symbol is a candidate,
not a verdict, and the `dev`-tagged tree is legitimately partial.

Requirements: Go 1.26+, [bats-core](https://github.com/bats-core/bats-core) for e2e tests.

## OAuth Development

For local development against BC3:
```bash
BASECAMP_BASE_URL=http://3.basecamp.localhost:3001 basecamp auth login
```

OAuth endpoints are discovered via `.well-known/oauth-authorization-server`.

## Benchmarks

```bash
make bench            # Run all benchmarks
make bench-cpu        # Run with CPU profiling
make bench-mem        # Run with memory profiling
make bench-save       # Save baseline for comparison
make bench-compare    # Compare against baseline
```

## SDK Sync

The CLI depends on the Basecamp SDK (`github.com/basecamp/basecamp-sdk/go`). When the
SDK adds new API operations, the CLI must add corresponding commands.

**Tracking**: `internal/version/sdk-provenance.json` records the SDK version +
API revision the CLI is built against. `API-COVERAGE.md` tracks endpoint coverage.

**Workflow** (see also `.claude/skills/sdk-bump.md`):
1. `make bump-sdk` — updates go.mod + provenance (never edit go.mod directly)
2. `go build ./...` — detect breaking changes
3. Fix any compilation errors in `internal/commands/` and `internal/sdk/`
4. Check for new SDK services/methods → create corresponding CLI commands
5. `make test` — verify all pass including catalog parity
6. Update `API-COVERAGE.md` with new endpoint coverage

**Completeness bar**: every new SDK service method needs:
- Command file in `internal/commands/`
- Catalog entry in `commands.go`
- Registration in `internal/cli/root.go` + `commands_test.go`
- API-COVERAGE.md row

**Andon cord**: if the SDK lacks a Go service wrapper for a generated endpoint,
stop and open an issue on [basecamp-sdk](https://github.com/basecamp/basecamp-sdk) —
never call the raw generated client from CLI code.

## Code style

See `STYLE.md` for the Go conventions used here. Read it when writing or reviewing Go;
it is not imported, so it stays out of context for sessions that never touch Go.
