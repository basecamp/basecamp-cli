# Contributing to Basecamp CLI

## Development Setup

```bash
git clone https://github.com/basecamp/basecamp-cli
cd basecamp-cli
bin/setup             # Install toolchain and dev tools
make build            # Build
bin/ci                # Verify everything passes
```

### Nix

As an alternative to `bin/setup`, contributors with Nix can enter the development environment with:

```bash
nix develop
```

The development shell provides Go and the tools required by `bin/ci`.

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

- Go 1.26.7+
- [bats-core](https://github.com/bats-core/bats-core) for integration tests
- [golangci-lint](https://golangci-lint.run/) for linting
- [jq](https://jqlang.github.io/jq/) for CLI surface checks
- Ruby 3.3 for the skill-eval pattern check, which compiles those patterns
  under the same engine the eval runner uses

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

### Running basecamp connect against a local Basecamp

The connector's end-to-end scenarios in `e2e/connect` run the real binary
against the fake Basecamp in `internal/connector/fakebasecamp` as part of
`make test`. The same scenarios also run against a local Basecamp (BC3 in
development). Passing on both is the check that the fake behaves as Basecamp
does.

Prerequisites, all against the local Basecamp:

1. The event feed is enabled for the account, and Action Cable is served, so
   `basecamp connect` can stream.
2. An operator profile, logged in as you:
   `basecamp profile create rob --base-url http://3.basecamp.localhost:3001`.
3. An untrusted profile, logged in as another person on the project. The
   agent takes no instructions from this person.
4. An agent connected under a profile of its own and set up in operator trust
   (the default) to serve the project:

   ```bash
   basecamp auth agent connect -P agent
   basecamp connect setup -P agent --operator-profile rob --serve <project-id>
   ```

5. No connector running for that agent. The scenarios stop with an error if
   `basecamp connect status -P agent` shows one, because that connector would
   get the scenarios' mentions too.

Then run:

```bash
BASECAMP_BASE_URL=http://3.basecamp.localhost:3001 \
BASECAMP_CONNECT_DEV_AGENT_PROFILE=agent \
BASECAMP_CONNECT_DEV_OPERATOR_PROFILE=rob \
BASECAMP_CONNECT_DEV_UNTRUSTED_PROFILE=jason \
BASECAMP_CONNECT_DEV_PROJECT_ID=<project-id> \
make test-connect-dev
```

The target fails, and does not skip, when a variable is missing. It also
refuses a `BASECAMP_BASE_URL` that is not `localhost`, a loopback address or a
`*.localhost` host, because the scenarios post as your profiles.

What it does:

- It uses your config and your credentials (keyring included) as they are.
  It never runs `auth agent connect` or `connect setup`, and it does not
  change `connect.json`. It reads `connect.json` with `basecamp connect show`.
- Each scenario gives its connector a temporary `XDG_STATE_HOME`. The
  connector's ledger and its per-agent instance lock are there. They are not
  yours, so the scenario cannot read or move your connector's position, and
  it cannot hold your lock.
- Each scenario posts a new message in the project as the operator, with
  nobody subscribed. It comments on that message with `basecamp comments
  create`, and mentions the agent as `[@agent](person:<id>)`. When the
  scenario ends, it trashes the message and its comments.
- The scenarios run one at a time.

What it covers: a trusted mention handed off in one request line (field by
field, from the comment Basecamp returned), an untrusted mention discarded as
`untrusted_performer`, stdout that holds only NDJSON, a clean SIGINT, a
second connector for the same agent refused at once, and a request admitted
under `--hold` that is handed off after a `kill -9`, `connect release` and a
restart.

What stays fake-only: the scenarios that need a fault or a world only the
fake can make. These are a dropped or silent live connection, the poll lane
and the live lane published apart, a rate-limited token endpoint, failed or
held recording reads, an agent disconnected or connected on another computer,
tokens that last a second, a second agent, and `connect setup` itself. Each
one calls `fakeOnly` with its reason. The run against the local Basecamp
shows them as skipped, with that reason.

## Questions?

Open an issue for questions about contributing.
