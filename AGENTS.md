# bcq Development Context

## Repository Structure

```
bcq/
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
│   ├── observability/# Tracing and metrics
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
make test-e2e         # Run BATS end-to-end tests
make check            # All checks (fmt-check, vet, lint, test, test-e2e)
```

Requirements: Go 1.25.6+, [bats-core](https://github.com/bats-core/bats-core) for e2e tests.

## OAuth Development

For local development against BC3:
```bash
BCQ_BASE_URL=http://3.basecamp.localhost:3001 bcq auth login
```

OAuth endpoints are discovered via `.well-known/oauth-authorization-server`.

## Benchmarks

**Go benchmarks:**
```bash
make bench            # Run all benchmarks
make bench-cpu        # Run with CPU profiling
make bench-mem        # Run with memory profiling
make bench-save       # Save baseline for comparison
make bench-compare    # Compare against baseline
```

**Skills benchmarking:** See `skills-benchmarking/` for agent task evaluation harness.
