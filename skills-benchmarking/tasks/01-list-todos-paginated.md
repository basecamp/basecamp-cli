# Task 01: List All Todos (Pagination Required)

## Objective

Retrieve ALL todos from the benchmark todolist. There are 75 seed todos, but the API returns a maximum of 50 per page by default. You must handle pagination to get all of them.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Todolist ID: `$BCQ_BENCH_TODOLIST_ID`
- Expected count: 75+ todos (seeded with "Benchmark Seed Todo 1" through "Benchmark Seed Todo 75")

## Success Criteria

Return a list containing all todos. The validation will check that at least 75 todos with content starting with "Benchmark Seed Todo" are returned.

## Instructions

1. Fetch todos from the benchmark todolist
2. Handle pagination if the API returns partial results
3. Output the complete list of todos

## Notes

- The Basecamp API default `per_page` is 50
- Use `?page=N` query parameter to fetch subsequent pages
- Continue fetching until an empty array is returned
- All 75+ todos should be in the final result
