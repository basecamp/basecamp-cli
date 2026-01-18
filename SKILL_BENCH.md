# Skill Benchmark Spec

Empirical test to determine the optimal skill strategy for bcq.

## Questions

1. **Does bcq help?** — Compare bcq strategies vs raw API strategies
2. **Full vs generated skill?** — Compare `bcq-full` vs `bcq-generated`
3. **Skill vs no skill?** — Compare skill-based vs `bcq-only`
4. **Does guidance help raw API?** — Compare `raw-docs` vs `raw-guided`

## Hypothesis

A minimal, CLI-generated skill may be sufficient if:
1. **Domain invariants** (bucket=project, todoset vs todolist) are the main value
2. **Full skill overhead** (efficiency contract, workflow patterns) doesn't improve outcomes

If generated skill matches full skill, we switch to generated (less maintenance, no drift).

## Strategies

| Strategy | Tools | What agent sees |
|-----------|-------|-----------------|
| `bcq-full` | bcq | Full hand-authored skill (control) |
| `bcq-generated` | bcq | Minimal CLI-generated skill |
| `bcq-only` | bcq | "use `bcq --help`" prompt only |
| `raw-docs` | curl | Raw API with docs link only |
| `raw-guided` | curl | Raw API with endpoint examples |

### bcq-full (control)

Agent receives:
- `benchmarks/skills/bcq-full/SKILL.md` — symlink to production skill
- Access to `bcq` CLI

The full skill provides:
- Efficiency contract ("≤2 tool calls")
- Agent mode documentation
- Workflow patterns (sweep, batch)
- Security rules
- Built-in features matrix

This is the current production skill — the control baseline.

### bcq-generated

Agent receives:
- `benchmarks/skills/bcq-generated/SKILL.md` (from `bcq skill`)
- Access to `bcq` CLI

The generated skill provides:
- Domain invariants (bucket=project, todoset vs todolist, etc.)
- Preferred patterns
- Anti-patterns to avoid

Tests if minimal CLI-generated skill is sufficient.

### bcq-only

Agent receives:
- `benchmarks/skills/bcq-only/SKILL.md` — minimal "use bcq --help" prompt
- Access to `bcq` CLI
- No domain hints

Tests whether `bcq --help` alone is self-sufficient.

### raw-docs

Agent receives:
- `benchmarks/skills/raw-docs/SKILL.md` — link to API docs only
- curl + jq tools
- No bcq CLI

The minimal raw skill provides:
- Authentication pattern
- Link to fetch API documentation

Baseline to measure bcq's value-add over pure docs.

### raw-guided

Agent receives:
- `benchmarks/skills/raw-guided/SKILL.md` — full endpoint examples
- curl + jq tools
- No bcq CLI

The guided raw skill provides:
- Authentication pattern
- Endpoint URLs and curl examples
- Response structure documentation
- Pagination and rate limiting notes

Tests whether endpoint guidance (without bcq) is sufficient.

## Strategy Map

Single source of truth: `benchmarks/strategies.json`

```json
{
  "strategies": {
    "bcq-full":      { "tools": ["bcq"], ... },
    "bcq-generated": { "tools": ["bcq"], ... },
    "bcq-only":      { "tools": ["bcq"], ... },
    "raw-docs":      { "tools": ["curl", "jq"], ... },
    "raw-guided":    { "tools": ["curl", "jq"], ... }
  }
}
```

## Tasks

Reuse tasks from `benchmarks/spec.yaml`:

| Task | Tests |
|------|-------|
| 01: List todos (paginated) | Pagination handling |
| 02: Find + complete todo | Chained operations |
| 03: Create + assign todo | Person lookup, assignment |
| 04: Comment on message | Recording types |
| 05: Reorder todo | Position API |
| 06: Create list + todos | Todoset vs todolist confusion |
| 07: Recover from 429 | Error handling |
| 08: Recover from 401 | Auth refresh |
| 09: Bulk complete overdue | Filter + bulk ops |
| 10: Cross-project search | Search API |

### Key differentiator tasks

| Task | Tests | Differentiates |
|------|-------|----------------|
| 06: Create list + todos | todoset vs todolist | bcq-full/generated vs bcq-only/raw |
| 09: Bulk complete overdue | Workflow patterns | bcq-full vs bcq-generated |
| 07: Recover from 429 | Error handling | bcq-* vs raw-* |
| 01: Pagination | Following Link headers | raw-docs vs raw-guided |

## Metrics

| Metric | Description |
|--------|-------------|
| `success_rate` | % of tasks completed correctly |
| `error_count` | Client errors (4xx) during task |
| `invariant_violations` | Domain errors (bucket/todoset confusion) |
| `help_invocations` | Times agent ran `bcq --help` |
| `time_to_success_ms` | Time from task start to validation pass |

## Execution

```bash
# Run all strategies
./benchmarks/harness.sh --strategy bcq-full
./benchmarks/harness.sh --strategy bcq-generated
./benchmarks/harness.sh --strategy bcq-only
./benchmarks/harness.sh --strategy raw-docs
./benchmarks/harness.sh --strategy raw-guided

# Compare results
jq -s 'group_by(.strategy) | map({
  strategy: .[0].strategy,
  success_rate: ([.[] | select(.success)] | length) / length,
  avg_errors: ([.[] | .metrics.error_count] | add / length),
  avg_time_ms: ([.[] | .metrics.time_ms] | add / length)
})' benchmarks/results/*.json
```

## Decision Criteria

### Question 1: Does bcq help?

| Outcome | Decision |
|---------|----------|
| bcq-* >> raw-* | bcq adds value, keep CLI |
| bcq-* ≈ raw-* | bcq not worth it |

### Question 2: Full vs generated skill?

| Outcome | Decision |
|---------|----------|
| bcq-full >> bcq-generated | Keep hand-authored |
| bcq-full ≈ bcq-generated | Switch to generated |

### Question 3: Skill vs no skill?

| Outcome | Decision |
|---------|----------|
| bcq-full/generated >> bcq-only | Skill adds value |
| bcq-full/generated ≈ bcq-only | `bcq --help` is sufficient |

### Question 4: Does guidance help raw API?

| Outcome | Decision |
|---------|----------|
| raw-guided >> raw-docs | Endpoint examples matter |
| raw-guided ≈ raw-docs | Docs link is sufficient |

### Decision matrix

| full vs generated | best vs only | best-bcq vs best-raw | Action |
|-------------------|--------------|----------------------|--------|
| full >> generated | full >> only | full >> raw | Ship full skill |
| full ≈ generated | generated >> only | generated >> raw | Ship generated skill |
| full ≈ generated | generated ≈ only | only >> raw | Ship bcq, no skill |
| — | — | only ≈ raw | Raw API sufficient |

## Files

| File | Purpose |
|------|---------|
| `lib/agent_invariants.json` | Source of truth for domain invariants |
| `benchmarks/strategies.json` | Strategy → skill mapping |
| `benchmarks/skills/bcq-full/SKILL.md` | Symlink to production skill |
| `benchmarks/skills/bcq-generated/SKILL.md` | CLI-generated skill |
| `benchmarks/skills/bcq-only/SKILL.md` | Minimal "use --help" |
| `benchmarks/skills/raw-docs/SKILL.md` | Docs link only |
| `benchmarks/skills/raw-guided/SKILL.md` | Endpoint examples |
| `bcq help` | Agent-optimized help |
| `bcq skill` | Skill generator |

## Promotion Strategy

All skills live in `benchmarks/skills/` until the benchmark decides.

After benchmark:
1. **If bcq-full wins**: Keep production skill as-is
2. **If bcq-generated wins**: Generate skill at install time
3. **If bcq-only wins**: No skill shipped, rely on `bcq --help`
4. **If raw-* wins**: bcq provides no value, reconsider

## Regenerating the Generated Skill

When invariants change:
```bash
bcq skill > benchmarks/skills/bcq-generated/SKILL.md
git add benchmarks/skills/bcq-generated/SKILL.md lib/agent_invariants.json
git commit -m "Update generated skill with new invariants"
```
