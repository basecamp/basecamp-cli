# Benchmarking Policy

This directory defines how benchmark runs are executed and what is considered
publishable. The goal is repeatable, defensible results with minimal noise.

## Canonical task

Task 12 (overdue sweep across two projects) is the current baseline because it
forces chained reads, pagination, filtering, comments, completions, and 429
recovery in one workflow.

Other tasks can be used for exploration, but should not be treated as canonical
unless explicitly promoted and re-run across the full matrix.

## Cohort integrity

All runs in a published report must share:
- `prompt_regime` (e.g., baseline_soft_anchor_env_today)
- `prompt_hash`
- `contract_version`

Mixed cohorts are invalid and must not be aggregated.

## Run validity gate

Only runs classified as `VALID` or `MODEL_FAILURE` count toward results.
All other classes (`HARNESS_BUG`, `INFRA_FLAKE`, `DATA_ISSUE`, `API_UNAVAILABLE`)
are excluded and must be rerun after fixes.

Run triage is required before report generation:
```
./harness/triage.sh --batch <results_dir>
```

## Minimum sample size

Final claims require N>=5 valid runs per model/condition cell. If N differs,
report per-model results only and do not aggregate.

## Canonical outputs

Publishable reports live in `benchmarks/reports/`.
Raw run artifacts live in `benchmarks/results/` and are not committed.

## Rerun policy

If prompts, validation rules, or cohort tags change, start a fresh results
directory and rerun the full matrix for that cohort.

## Quality gates

Gates are policy, not CI-enforced (no API keys in CI).

| Gate | When | What |
|------|------|------|
| **Smoke** | Any CLI/skill change | Task 12, bcq condition, Haiku, 1 run. Must pass. |
| **Regression** | Before releases | Full matrix (N>=5 per cell). Compare to `reports/baseline.json`. |
| **Benchmark refresh** | Prompt/regime/model changes | Full matrix + update baseline. |

### Running the smoke gate

```bash
source env.sh
./harness/run.sh --task 12 --condition bcq --model claude-haiku
# Expect: success=true in metrics.json
```

### Running the regression gate

```bash
source env.sh
./harness/matrix.sh --task 12 --models "claude-sonnet,claude-haiku,gpt-5-mini" \
  --conditions "bcq,raw" --runs 5
./harness/triage.sh --batch results/
# Compare pass rates to reports/baseline.json
```

## Baseline reference

- `reports/baseline.json` — machine-readable summary of canonical results
- `reports/BENCHMARK-REPORT-FINAL.md` — human-readable report
