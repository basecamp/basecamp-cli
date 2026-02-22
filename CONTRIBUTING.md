# Contributing to bcq

## Development Setup

```bash
# Clone
git clone https://github.com/basecamp/basecamp-cli
cd basecamp-cli

# Install dev tools (golangci-lint, govulncheck, etc.)
make tools

# Build
make build

# Run tests
make test        # Go unit tests
make test-e2e    # End-to-end tests (requires bats-core)

# Run all checks
make check       # fmt-check, vet, lint, test, test-e2e
```

## SDK Development

When developing against a local copy of [basecamp-sdk](https://github.com/basecamp/basecamp-sdk), use Go workspaces instead of `replace` directives in go.mod:

```bash
# Set up workspace (one-time)
go work init .
go work use ../basecamp-sdk/go

# Now bcq will use your local SDK automatically
go build ./...
```

The `go.work` file is gitignored - your local setup won't affect the repo.

## Requirements

- Go 1.26+
- [bats-core](https://github.com/bats-core/bats-core) for integration tests
- [golangci-lint](https://golangci-lint.run/) for linting

## Pull Request Process

1. **Run checks locally** before pushing:
   ```bash
   make check
   make lint
   ```

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
├── cmd/bcq/          # Main entrypoint
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
BCQ_BIN=./bin/bcq bats e2e/
```

## Questions?

Open an issue for questions about contributing.
