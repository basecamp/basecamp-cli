# Contributing to Basecamp CLI

## Development Setup

```bash
git clone https://github.com/basecamp/basecamp-cli
cd basecamp-cli
bin/setup             # Install toolchain and dev tools
make build            # Build
bin/ci                # Verify everything passes
```

## SDK Development

When developing against a local copy of [basecamp-sdk](https://github.com/basecamp/basecamp-sdk), use Go workspaces instead of `replace` directives in go.mod:

```bash
# Set up workspace (one-time)
go work init .
go work use ../basecamp-sdk/go

# Now basecamp will use your local SDK automatically
go build ./...
```

The `go.work` file is gitignored - your local setup won't affect the repo.

## Requirements

- Go 1.26+
- [bats-core](https://github.com/bats-core/bats-core) for integration tests
- [golangci-lint](https://golangci-lint.run/) for linting
- [jq](https://jqlang.github.io/jq/) for CLI surface checks

## Pull Request Process

1. **Run CI locally** before pushing:
   ```bash
   bin/ci
   ```
   This runs formatting, vetting, linting, unit tests, e2e tests, naming checks,
   CLI surface checks, provenance checks, and tidy checks. Fix anything that fails
   before pushing.

2. **Add tests** for new functionality

3. **Update documentation** if adding commands or changing behavior

4. **Keep commits focused** - one logical change per commit

## Code Style

- Run `make fmt` before committing
- Follow [Effective Go](https://go.dev/doc/effective_go) conventions
- Follow [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)

## Project Structure

```
basecamp-cli/
├── cmd/basecamp/     # Main entrypoint
├── internal/
│   ├── auth/         # OAuth authentication
│   ├── commands/     # CLI command implementations
│   ├── config/       # Configuration management
│   ├── output/       # Output formatting
│   └── sdk/          # Basecamp SDK wrapper
└── e2e/              # BATS end-to-end tests
```

## Testing

### Unit Tests (Go)

```bash
make test
```

### End-to-End Tests (BATS)

Requires [bats-core](https://github.com/bats-core/bats-core):

```bash
brew install bats-core  # macOS
make test-e2e
```

### Running Against Go Binary

```bash
BASECAMP_BIN=./bin/basecamp bats e2e/
```

## Questions?

Open an issue for questions about contributing.
