# Contributing to bcq

## Development Setup

```bash
# Clone
git clone https://github.com/basecamp/bcq
cd bcq

# Build
make build

# Run tests
make test        # Go unit tests
make test-bats   # Integration tests (requires bats-core)

# Run all checks
make check       # fmt-check, vet, test
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

- Go 1.25+
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
bcq/
├── cmd/bcq/          # Main entrypoint
├── internal/
│   ├── api/          # Basecamp API client
│   ├── auth/         # OAuth authentication
│   ├── commands/     # CLI command implementations
│   ├── config/       # Configuration management
│   └── output/       # Output formatting (JSON, tables)
└── test/             # BATS integration tests
```

## Testing

### Unit Tests (Go)

```bash
make test
```

### Integration Tests (BATS)

Requires [bats-core](https://github.com/bats-core/bats-core):

```bash
brew install bats-core  # macOS
make test-bats
```

### Running Against Go Binary

```bash
BCQ_BIN=./bin/bcq bats test/
```

## Questions?

Open an issue for questions about contributing.
