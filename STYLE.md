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

## File Organization

One file per top-level command group in `internal/commands/`.
Shortcut commands (e.g., `todo`, `done`, `comment`) live alongside their parent group.
