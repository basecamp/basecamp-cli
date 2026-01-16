# Task 03: Create Todo with Person Assignment

## Objective

Create a new todo titled "Test Assignment" and assign it to a person.

## Context

- Project ID: `$BCQ_BENCH_PROJECT_ID`
- Todolist ID: `$BCQ_BENCH_TODOLIST_ID`
- Person ID: `$BCQ_BENCH_PERSON_ID` (already provided)

## Success Criteria

A todo with content "Test Assignment" exists in the todolist and has at least one assignee.

## Instructions

1. Create a todo with content "Test Assignment"
2. Assign it to `$BCQ_BENCH_PERSON_ID`

## Notes

- The person ID is already available in the environment variable
- The assignment must be set at creation time using `assignee_ids`
- API endpoint: `POST /buckets/{project_id}/todolists/{todolist_id}/todos.json`
