# Task 06: Create Todolist and Add 3 Todos

## Objective

Create a new todolist named "Benchmark List" and add 3 todos to it.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Todoset ID: `$BCQ_BENCH_TODOSET_ID` (parent container for todolists)

## Success Criteria

A todolist named "Benchmark List" exists with exactly 3 todos.

## Instructions

1. Create a new todolist named "Benchmark List" in the project's todoset
2. Add 3 todos to the newly created list (any content is fine)

## Notes

- Todolists are created under a todoset: `POST /buckets/{project_id}/todosets/{todoset_id}/todolists.json`
- You'll need the new todolist's ID to add todos to it
- This is a chained write operation: create list → create 3 todos
