# Basecamp CLI Development Context

## Getting Started

See [CONTRIBUTING.md](CONTRIBUTING.md) for build setup, testing, and PR workflow.

## Repository Structure

```
basecamp-cli/
├── cmd/bcq/          # Main entrypoint
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
└── .claude-plugin/   # Claude Code integration
```

## Basecamp API Reference

API documentation: https://github.com/basecamp/bc3-api

Key endpoints used by bcq:
- `/projects.json` - List projects
- `/buckets/{id}/todolists/{id}/todos.json` - Todos in a list
- `/buckets/{id}/todos/{id}/completion.json` - Complete todo
- `/people.json` - List people
- `/my/profile.json` - Current user

**Search:** Use `bcq search "query"` for full-text search across projects. The Recordings API (`bcq recordings`) is for browsing by type/status without a search term.

## Testing

```bash
make build            # Build binary to ./bin/bcq
make test             # Run Go unit tests
make test-e2e         # Run BATS end-to-end tests
make check            # All checks (fmt-check, vet, lint, test, test-e2e)
```

Requirements: Go 1.25.7+, [bats-core](https://github.com/bats-core/bats-core) for e2e tests.

## OAuth Development

For local development against BC3:
```bash
BCQ_BASE_URL=http://3.basecamp.localhost:3001 bcq auth login
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
- Registration in `root.go` + `commands_test.go`
- API-COVERAGE.md row

**Andon cord**: if the SDK lacks a Go service wrapper for a generated endpoint,
stop and open an issue on [basecamp-sdk](https://github.com/basecamp/basecamp-sdk) —
never call the raw generated client from CLI code.
