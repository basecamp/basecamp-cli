# Basecamp CLI Style Guide

Conventions for contributors and agents working on basecamp-cli.

## Command Constructors

Exported `NewXxxCmd() *cobra.Command` — one per top-level command group in `internal/commands/`.
Subcommands are unexported `newXxxYyyCmd()` added via `cmd.AddCommand()`.

## Output

Success: `app.OK(data, ...options)` with optional `WithBreadcrumbs`, `WithSummary`, `WithContext`.
Errors: `output.ErrUsage()`, `output.ErrNotFound()`, SDK error conversion via `output.AsError()`.

## Config Resolution

6-layer precedence: flags > env > local > repo > global > system > defaults.
Trust boundaries enforced via `config.TrustStore`.
Source tracking via `cfg.Sources["field_name"]` records provenance of each value.

## Catalog

Static `commandCategories()` in `commands.go`. Every registered command must appear.
`TestCatalogMatchesRegisteredCommands` enforces bidirectional parity.

## Method Ordering

Invocation order: constructor, RunE, then helpers called by RunE.
Export order: public before private.

## Bare Command Groups

Command groups (resource nouns like `todos`, `projects`, `todosets`) must not set
`RunE` on the parent command. Bare invocation shows help — Cobra does this
automatically when a command has subcommands and no `RunE`.

Singular action shortcuts that shadowed a plural group noun (`card`, `todo`,
`done`, `reopen`, `message`, `comment`, `react`) have been removed — always use
the canonical `<group> <action>` form (`cards create`, `todos create`,
`todos complete`, etc.). Introducing a new top-level verb that shadows an
existing group noun is not allowed.

Shortcut commands without a sibling plural group — `search`, `url`,
`recordings`, `timesheet`, `assignments`, `notifications`, `setup`, `completion`
— may have both `RunE` and subcommands. `scripts/check-bare-groups.sh` enforces
this with an allowlist.

## File Organization

One file per top-level command group in `internal/commands/`.

## Import Ordering

Three groups separated by blank lines, each alphabetically sorted:
1. Standard library
2. Third-party modules
3. Project-internal (`github.com/basecamp/cli/...`)

`goimports` enforces this.

## Testing

Prefer `assert`/`require` from testify. Helper functions over table-driven tests
unless tabular form is clearly better. Skip assertion descriptions when the
default failure message suffices.
