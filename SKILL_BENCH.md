# Skill Benchmark Spec

Empirical test to determine the optimal skill strategy for bcq.

## Questions

1. **Does bcq help?** — Compare bcq strategies vs raw API strategies
2. **Full vs generated skill?** — Compare `bcq-full` vs `bcq-generated`
3. **Skill vs no skill?** — Compare skill-based vs `bcq-only`
4. **Does guidance help raw API?** — Compare `api-docs-only` vs `api-docs-with-curl-examples`

## Hypothesis

A minimal, CLI-generated skill may be sufficient if:
1. **Domain invariants** (bucket=project, todoset vs todolist) are the main value
2. **Full skill overhead** (efficiency contract, workflow patterns) doesn't improve outcomes

If generated skill matches full skill, we switch to generated (less maintenance, no drift).

## Strategies

| Strategy | Tools | Type | What agent sees |
|-----------|-------|------|-----------------|
| `bcq-full` | bcq | skill | Full hand-authored skill (control) |
| `bcq-generated` | bcq | skill | Minimal CLI-generated skill |
| `bcq-only` | bcq | prompt | "use `bcq --help`" prompt only |
| `api-docs-only` | curl | prompt | Raw API with docs link only |
| `api-docs-with-curl-examples` | curl | prompt | Raw API with endpoint examples |

### bcq-full (control)

Agent receives:
- `skills/basecamp/SKILL.md` — production skill via Claude Code skill system
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
- `benchmarks/skills/bcq-generated/SKILL.md` — CLI-generated skill
- Access to `bcq` CLI

The generated skill provides:
- Domain invariants (bucket=project, todoset vs todolist, etc.)
- Preferred patterns
- Anti-patterns to avoid

Tests if minimal CLI-generated skill is sufficient (parity with bcq-full as a skill).

### bcq-only

Agent receives:
- `benchmarks/prompts/bcq-only.md` — minimal "use bcq --help" prompt
- Access to `bcq` CLI
- No domain hints

Tests whether `bcq --help` alone is self-sufficient.

### api-docs-only

Agent receives:
- `benchmarks/prompts/api-docs-only.md` — link to API docs only
- curl + jq tools
- No bcq CLI

The minimal raw prompt provides:
- Authentication pattern
- Link to fetch API documentation

Baseline to measure bcq's value-add over pure docs.

### api-docs-with-curl-examples

Agent receives:
- `benchmarks/prompts/api-docs-with-curl-examples.md` — full endpoint examples
- curl + jq tools
- No bcq CLI

The guided raw prompt provides:
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
    "bcq-full":      { "skill": "skills/basecamp/SKILL.md", "tools": ["bcq"] },
    "bcq-generated": { "skill": "benchmarks/skills/bcq-generated/SKILL.md", "tools": ["bcq"] },
    "bcq-only":      { "prompt": "benchmarks/prompts/bcq-only.md", "tools": ["bcq"] },
    "api-docs-only":      { "prompt": "benchmarks/prompts/api-docs-only.md", "tools": ["curl", "jq"] },
    "api-docs-with-curl-examples":    { "prompt": "benchmarks/prompts/api-docs-with-curl-examples.md", "tools": ["curl", "jq"] }
  }
}
```

Strategies use either:
- **skill**: Points to a SKILL.md file loaded by Claude Code's skill system (bcq-full, bcq-generated)
- **prompt**: Points to a prompt file prepended to the task (bcq-only, api-docs-only, api-docs-with-curl-examples)

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
| 06: Create list + todos | todoset vs todolist | bcq-full/generated vs bcq-only/api-* |
| 09: Bulk complete overdue | Workflow patterns | bcq-full vs bcq-generated |
| 07: Recover from 429 | Error handling | bcq-* vs api-* |
| 01: Pagination | Following Link headers | api-docs-only vs api-docs-with-curl-examples |

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
./benchmarks/harness.sh --task all --strategy bcq-full
./benchmarks/harness.sh --task all --strategy bcq-generated
./benchmarks/harness.sh --task all --strategy bcq-only
./benchmarks/harness.sh --task all --strategy api-docs-only
./benchmarks/harness.sh --task all --strategy api-docs-with-curl-examples

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
| bcq-* >> api-* | bcq adds value, keep CLI |
| bcq-* ≈ api-* | bcq not worth it |

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
| api-docs-with-curl-examples >> api-docs-only | Endpoint examples matter |
| api-docs-with-curl-examples ≈ api-docs-only | Docs link is sufficient |

### Decision matrix

| full vs generated | best vs only | best-bcq vs best-api | Action |
|-------------------|--------------|----------------------|--------|
| full >> generated | full >> only | full >> api | Ship full skill |
| full ≈ generated | generated >> only | generated >> api | Ship generated prompt |
| full ≈ generated | generated ≈ only | only >> api | Ship bcq, no skill |
| — | — | only ≈ api | Raw API sufficient |

## Files

| File | Purpose |
|------|---------|
| `lib/agent_invariants.json` | Source of truth for domain invariants |
| `benchmarks/strategies.json` | Strategy → skill/prompt mapping |
| `skills/basecamp/SKILL.md` | Production skill (bcq-full) |
| `benchmarks/skills/bcq-generated/SKILL.md` | CLI-generated skill |
| `benchmarks/prompts/bcq-only.md` | Minimal "use --help" |
| `benchmarks/prompts/api-docs-only.md` | Docs link only |
| `benchmarks/prompts/api-docs-with-curl-examples.md` | Endpoint examples |
| `bcq help` | Agent-optimized help |
| `bcq skill` | Skill generator |

## Promotion Strategy

Benchmark results determine which strategy to ship:

1. **If bcq-full wins**: Keep production skill as-is
2. **If bcq-generated wins**: Ship generated prompt instead of skill
3. **If bcq-only wins**: No skill shipped, rely on `bcq --help`
4. **If api-* wins**: bcq provides no value, reconsider

## Skill Promotion Rule

**Rule:** If `bcq-only` achieves ≥95% of `bcq-full` pass rate on a task, don't ship a skill for that task.

Rationale: The skill adds maintenance burden without measurable reliability lift. `bcq --help` is self-sufficient.

### Task 12 Results (2026-01-18)

| Strategy | Pass Rate |
|----------|-----------|
| bcq-full | 100% |
| bcq-generated | 100% |
| bcq-only | 100% |
| api-docs-only | 55% |
| api-docs-with-curl-examples | 45% |

**Decision for Task 12:** bcq-only matches bcq-full (100% = 100%). No skill needed for overdue sweep workflows.

## Regenerating the Generated Skill

When invariants change:
```bash
bcq skill > benchmarks/skills/bcq-generated/SKILL.md
git add benchmarks/skills/bcq-generated/SKILL.md lib/agent_invariants.json
git commit -m "Update generated skill with new invariants"
```
