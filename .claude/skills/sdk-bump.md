---
description: Bump the Basecamp SDK dependency and adapt CLI code to any breaking changes
user_invocable: true
---

# SDK Bump Workflow

Bump the Basecamp SDK (`github.com/basecamp/basecamp-sdk/go`) to a newer revision and handle any breaking changes.

## Steps

1. **Read baseline**: Read `internal/version/sdk-provenance.json` to understand the current SDK version.

2. **Run bump**: Execute `make bump-sdk` (or `make bump-sdk REF=<specific-ref>` if a specific revision was requested).

3. **Compile**: Run `go build ./...` to detect breaking changes from the SDK update.

4. **Fix breakage**: If compilation fails, identify which SDK types or methods changed and adapt the CLI code:
   - Check `go/pkg/generated/client.gen.go` in the SDK for new type/method signatures
   - Update hand-written service calls in `internal/commands/` and `internal/sdk/`
   - Never bypass the generated client — see AGENTS.md SDK Development Principles

5. **Adopt new features**: Compare the old and new SDK to identify new services or methods:
   - Check for new `*Service` types in `go/pkg/basecamp/`
   - Check for new `AccountClient` accessor methods (e.g., `Boosts()`)
   - For each new service: create a CLI command following existing patterns
   - Register new commands: `root.go`, `commands.go` catalog, `commands_test.go`
   - Update `API-COVERAGE.md` with new endpoint coverage
   - If the SDK has generated client methods but NO hand-written service wrapper,
     stop and file a communique — the SDK needs the service layer first

6. **Run tests**: Execute `make test` to verify everything passes.

7. **Summarize**: Compare the old and new `sdk-provenance.json` to summarize what changed (SDK version delta, API revision changes if available). Use this for the commit message.
