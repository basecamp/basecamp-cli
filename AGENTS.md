# bcq Development Context

## Repository Structure

```
bcq/
├── cmd/bcq/          # Main entrypoint
├── internal/
│   ├── api/          # Basecamp API client
│   ├── appctx/       # Application context
│   ├── auth/         # OAuth authentication
│   ├── cli/          # CLI framework
│   ├── commands/     # Command implementations
│   ├── config/       # Configuration management
│   ├── dateparse/    # Date parsing
│   ├── names/        # Name resolution
│   ├── output/       # Output formatting
│   ├── tui/          # Terminal UI
│   └── version/      # Version info
├── test/             # BATS integration tests
└── .claude-plugin/   # Claude Code integration
```

## Basecamp API Reference

API documentation: `~/Work/basecamp/bc3-api` (local clone)

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
make test-bats        # Run BATS integration tests
make check            # All checks (fmt-check, vet, test)
```

Requirements: Go 1.25+, [bats-core](https://github.com/bats-core/bats-core) for integration tests.

## OAuth Development

For local development against BC3:
```bash
BCQ_BASE_URL=http://3.basecamp.localhost:3001 bcq auth login
```

OAuth endpoints are discovered via `.well-known/oauth-authorization-server`.

## Benchmarks

**Credentials:** Before running benchmarks, source the env file:
```bash
source benchmarks/.env   # Loads ANTHROPIC_API_KEY, OPENAI_API_KEY from 1Password
```

**Run benchmarks:**
```bash
./benchmarks/reset.sh                                          # Reset fixtures
./benchmarks/harness/run.sh --strategy <name> --task 12 --model claude-sonnet
./benchmarks/harness/matrix.sh                                 # Full matrix run
./benchmarks/harness/triage.sh --update benchmarks/results/    # Classify results
./benchmarks/harness/report.sh benchmarks/results/             # Generate report
```

**Strategies:** See `benchmarks/strategies.json` for available strategies (bcq-full, bcq-minimal, api-docs-with-agent-invariants, etc.)
