# Task 09: Find and Complete Overdue Todos

## Objective

Find all overdue todos in the benchmark todolist and mark each one as complete.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Todolist ID: `$BCQ_BENCH_TODOLIST_ID`
- Expected: 3 overdue todos (seeded with past due dates)
- Overdue todos have content like "Benchmark Overdue Todo 1", "Benchmark Overdue Todo 2", etc.

## Success Criteria

All 3 overdue todos (those with `due_on` in the past and content starting with "Benchmark Overdue Todo") should be marked as completed.

## Instructions

1. List todos in the benchmark todolist
2. Filter for todos that are:
   - Overdue (due_on < today)
   - Not already completed
   - Content starts with "Benchmark Overdue Todo"
3. Complete each one

## Notes

- This is a bulk operation: query → filter → multiple actions
- You need to check `due_on` field against today's date
- Each todo must be completed individually
- There are exactly 3 seeded overdue todos
