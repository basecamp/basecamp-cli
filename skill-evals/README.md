# Basecamp skill evals

These evals test whether an agent can turn Basecamp requests into correct CLI
calls. Cases assert tool-call traces and, where needed, the final answer.

The harness serves `--agent --help` from the locally built CLI. This lets the
skill rely on live command discovery instead of embedding a duplicate command
manual. All other command results remain deterministic case mocks.

## Deterministic checks

These do not call a model or require credentials:

```bash
make check-eval-patterns   # compile every case regex
make check-eval-harness    # build the CLI and verify live structured help
make check-skill-drift     # validate command and flag references in the skill
```

The Go documentation contract also enforces a 16 KiB budget for the main skill
and verifies that it remains discovery-first.

## Model evals

Model evals require `ANTHROPIC_API_KEY`:

```bash
make skill-eval
```

That target builds the CLI first, runs the main skill cases, then runs the
separate `basecamp-connect` cases.

To compare a proposed skill with `main`:

```bash
make build
git show origin/main:skills/basecamp/SKILL.md > /tmp/basecamp-skill-main.md
make -C skill-evals eval-save SKILL=/tmp/basecamp-skill-main.md NAME=main
make -C skill-evals eval-compare NAME=main
```

Pass `MODEL=...` to the `make -C skill-evals eval*` targets when testing another
model. Pass `--samples` directly to `skill-evals/run` when reducing variance.
