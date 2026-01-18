# Skill Benchmark Spec

Empirical test to determine the optimal skill strategy for bcq.

## Questions

1. **Does bcq help?** — Compare bcq-based conditions (A/B/C) vs raw API (D)
2. **Full vs generated skill?** — Compare hand-authored (A) vs CLI-generated (B)
3. **Skill vs no skill?** — Compare skill-based (A/B) vs `bcq --help` only (C)

## Hypothesis

A minimal, CLI-generated skill may be sufficient if:
1. **Domain invariants** (bucket=project, todoset vs todolist) are the main value
2. **Full skill overhead** (efficiency contract, workflow patterns) doesn't improve outcomes

If generated skill matches full skill, we switch to generated (less maintenance, no drift).

## Conditions

| Condition | Skill | CLI | What agent sees |
|-----------|-------|-----|-----------------|
| **A** | `bcq-full` | bcq | Full hand-authored skill (control) |
| **B** | `bcq-generated` | bcq | Minimal CLI-generated skill |
| **C** | `bcq-only` | bcq | "use `bcq --help`" prompt only |
| **D** | `basecamp-raw` | curl | Raw API skill with endpoint docs |

### Condition A: bcq + full skill (control)

Agent receives:
- `benchmarks/skills/bcq-full/SKILL.md` — existing hand-authored skill
- Access to `bcq` CLI

The full skill provides:
- Efficiency contract ("≤2 tool calls")
- Agent mode documentation
- Workflow patterns (sweep, batch)
- Security rules
- Built-in features matrix

This is the current production skill — the control baseline.

### Condition B: bcq + generated skill

Agent receives:
- `benchmarks/skills/bcq-generated/SKILL.md` (from `bcq help --agent --format=skill`)
- Access to `bcq` CLI

The generated skill provides:
- Domain invariants (bucket=project, todoset vs todolist, etc.)
- Preferred patterns
- Anti-patterns to avoid

Tests if minimal CLI-generated skill is sufficient.

### Condition C: bcq only

Agent receives:
- `benchmarks/skills/bcq-only/SKILL.md` — minimal "use bcq --help" prompt
- Access to `bcq` CLI
- No domain hints

Tests whether `bcq --help` alone is self-sufficient.

### Condition D: raw API skill

Agent receives:
- `benchmarks/skills/basecamp-raw/SKILL.md` with API endpoint documentation
- curl + jq tools
- No bcq CLI

The raw skill provides:
- Authentication pattern
- Endpoint URLs and curl examples
- Response structure documentation
- Pagination and rate limiting notes

Baseline to measure bcq's value-add over raw API.

## Tasks

Reuse tasks from `benchmarks/spec.yaml` (the existing bcq vs raw benchmark):

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
| 06: Create list + todos | todoset vs todolist | A/B vs C/D (invariants) |
| 09: Bulk complete overdue | Workflow patterns | A vs B (full skill features) |
| 07: Recover from 429 | Error handling | A/B/C vs D (bcq vs raw) |

If B matches A on task 09, the full skill's workflow patterns don't add value.
If B outperforms C on task 06, the generated skill's invariants help.

## Metrics

| Metric | Description |
|--------|-------------|
| `success_rate` | % of tasks completed correctly |
| `error_count` | Client errors (4xx) during task |
| `invariant_violations` | Times agent made a domain error (bucket/todoset confusion) |
| `help_invocations` | Times agent ran `bcq --help` or `bcq <cmd> --help` |
| `time_to_success_ms` | Time from task start to validation pass |

## Execution

```bash
# Run all four conditions
./benchmarks/harness.sh --condition bcq-full       # A: full skill (control)
./benchmarks/harness.sh --condition bcq-generated  # B: generated skill
./benchmarks/harness.sh --condition bcq-only       # C: bcq --help only
./benchmarks/harness.sh --condition raw            # D: raw API

# Compare results
jq -s 'group_by(.condition) | map({
  condition: .[0].condition,
  success_rate: ([.[] | select(.success)] | length) / length,
  avg_errors: ([.[] | .metrics.error_count] | add / length),
  avg_time_ms: ([.[] | .metrics.time_ms] | add / length)
})' benchmarks/results/*.json
```

## Decision Criteria

### Question 1: Does bcq help? (A/B/C vs D)

| Outcome | Decision |
|---------|----------|
| A/B/C >> D | bcq adds value, keep CLI |
| A/B/C ≈ D | bcq not worth it, raw API is fine |

### Question 2: Full vs generated skill? (A vs B)

| Outcome | Decision |
|---------|----------|
| A >> B | Full skill wins, keep hand-authored |
| A ≈ B | Generated skill sufficient, switch to it |
| A < B | Full skill harmful, investigate |

### Question 3: Skill vs no skill? (A/B vs C)

| Outcome | Decision |
|---------|----------|
| A or B >> C | Skill adds value |
| A or B ≈ C | `bcq --help` is sufficient, no skill needed |

### Decision matrix

| A vs B | Best(A,B) vs C | Best(A,B,C) vs D | Action |
|--------|----------------|------------------|--------|
| A >> B | A >> C | A >> D | Ship full skill |
| A ≈ B | B >> C | B >> D | Ship generated skill |
| A ≈ B | B ≈ C | C >> D | Ship bcq, no skill |
| — | — | C ≈ D | Raw API sufficient |

If generated skill wins:
```bash
# Promote to official skill
mv benchmarks/skills/bcq-generated skills/basecamp
git add skills/basecamp

# Add to installer:
bcq help --agent --format=skill > ~/.config/basecamp/skills/basecamp/SKILL.md
```

## Files

| File | Purpose |
|------|---------|
| `lib/agent_invariants.json` | Source of truth for domain invariants |
| `benchmarks/skills/bcq-full/SKILL.md` | Condition A: full hand-authored skill |
| `benchmarks/skills/bcq-generated/SKILL.md` | Condition B: CLI-generated skill |
| `benchmarks/skills/bcq-only/SKILL.md` | Condition C: minimal "use --help" |
| `benchmarks/skills/basecamp-raw/SKILL.md` | Condition D: raw API |
| `bcq help --agent` | Human-readable agent help |
| `bcq help --agent --format=skill` | Skill generator (produces B) |
| `bcq help --agent --format=json` | Machine-readable for tooling |

## Promotion Strategy

All skills live in `benchmarks/skills/` until the benchmark decides.

After benchmark:
1. **If A wins**: Keep `.claude-plugin/skills/bcq-basecamp/` as-is
2. **If B wins**: Generate skill at install time, ship to `skills/basecamp/`
3. **If C wins**: No skill shipped, rely on `bcq --help`
4. **If D wins**: bcq provides no value, reconsider CLI approach

## Regenerating the Generated Skill

When invariants change:
```bash
bcq help --agent --format=skill > benchmarks/skills/bcq-generated/SKILL.md
git add benchmarks/skills/bcq-generated/SKILL.md lib/agent_invariants.json
git commit -m "Update generated skill with new invariants"
```

The generated skill header includes:
```
# GENERATED by bcq X.Y.Z — do not edit
# Regenerate with: bcq help --agent --format=skill
```
