# bcq Development Context

## Repository Structure

```
bcq/
├── bin/bcq              # Main CLI entrypoint
├── lib/
│   ├── core.sh          # Output formatting, date parsing, utilities
│   ├── config.sh        # Config file management, credentials
│   ├── api.sh           # HTTP helpers, rate limiting, auth
│   ├── auth.sh          # OAuth 2.1 + DCR authentication
│   └── commands/        # Command implementations
├── test/                # bats tests
└── .claude-plugin/      # Claude Code integration
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
./test/run.sh         # Run all tests
bats test/*.bats      # Alternative: run bats directly
bats test/auth.bats   # Run auth tests only
```

Tests use [bats-core](https://github.com/bats-core/bats-core). Install with `brew install bats-core`.

## OAuth Development

For local development against BC3:
```bash
BCQ_BASE_URL=http://3.basecamp.localhost:3001 bcq auth login
```

OAuth endpoints are discovered via `.well-known/oauth-authorization-server`.
