# Task 12: Overdue Sweep Across Benchmark Projects

## Task

Find and complete all overdue todos across two benchmark projects using bcq.

## Efficiency Anchor

Efficient solutions exist that finish in **~1–2 bash calls** using bcq's batch operations.

**Correctness beats speed**: you must still process both projects and include the exact run ID in comments.

## Fast Path

```bash
cd /Users/jeremy/Work/basecamp/bcq/benchmarks && source env.sh && \
  ../bin/bcq todos sweep --overdue --in "$BCQ_BENCH_PROJECT_ID" \
    --comment "Processed BenchChain $BCQ_BENCH_RUN_ID" --complete --json && \
  ../bin/bcq todos sweep --overdue --in "$BCQ_BENCH_PROJECT_ID_2" \
    --comment "Processed BenchChain $BCQ_BENCH_RUN_ID" --complete --json
```

## Requirements

- Comment MUST include the **expanded value** of `$BCQ_BENCH_RUN_ID` (for validation)
  - Use double quotes: `--comment "Processed BenchChain $BCQ_BENCH_RUN_ID"`
  - NOT single quotes (prevents expansion): `--comment 'Processed BenchChain $BCQ_BENCH_RUN_ID'` ❌
- Both projects must be swept
- Report the swept/completed counts from the JSON output
