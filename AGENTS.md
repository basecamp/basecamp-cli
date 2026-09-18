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
`codex.go`). Grok Build has no plugin: it reads the shared `~/.agents/skills/basecamp` skill
directly, so it is a row of `harness.SkillAgent` (name, id, home env var, home directory,
binary) in `skill_agent.go`, and everything in `internal/commands` that touches a shared-skill
agent — the setup handler, the `BASECAMP_SETUP_AGENT` values, doctor's remediation — loops over
`harness.SkillAgents()` rather than naming it. A new shared-skill agent is a new row; the skill
picker's `(Global)` row, the prose lists in the installers and the docs are the places to update
by hand. `setup <id>` never fabricates an agent's home directory: a skill-only agent that is not
detected is reported missing, not created.

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
`skills/basecamp-doctor/SKILL.md` and `skills/basecamp-connect/SKILL.md`, checking that the commands and flags each one
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
