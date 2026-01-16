# Task 05: Reorder Todo Within List

## Objective

Move "Benchmark Todo Beta" to position 1 (top) within its todolist.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Todolist ID: `$BCQ_BENCH_TODOLIST_ID`
- Target todo: "Benchmark Todo Beta" (ID: `$BCQ_BENCH_TODO_BETA_ID`)

## Success Criteria

"Benchmark Todo Beta" should be the first todo in the list when queried.

## Instructions

1. Find the todo named "Benchmark Todo Beta" (or use `$BCQ_BENCH_TODO_BETA_ID`)
2. Update its position to 1

## Notes

- The API endpoint is `PUT /buckets/{project_id}/todos/{id}/position.json`
- Request body: `{"position": 1}`
- Position 1 means first in the list
- The todo is seeded at the end of the list initially
