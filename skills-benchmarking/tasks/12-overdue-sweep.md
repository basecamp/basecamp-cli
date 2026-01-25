# Task 12: Overdue Sweep Across Benchmark Projects

## Objective

Across all benchmark projects, find overdue todos and process them.

## Inputs

- Benchmark projects: `$BCQ_BENCH_PROJECT_ID` and `$BCQ_BENCH_PROJECT_ID_2`
- Run ID: `$BCQ_BENCH_RUN_ID` (use this exact value in comments for validation)
- Marker pattern: `Processed BenchChain <run_id>` (must include the run ID)

## Requirements

1. For each benchmark project:
   - Retrieve all todos across all todolists (must handle pagination).
   - Filter todos where:
     - `title` (or `content`) starts with **"Benchmark Overdue Todo"**
     - `due_on` < today
     - `completed == false`
2. For each matching todo:
   - Add a comment: `Processed BenchChain ${BCQ_BENCH_RUN_ID}`
   - Mark it complete

**IMPORTANT**: The comment MUST include the exact run ID (`$BCQ_BENCH_RUN_ID`) for validation to pass.

## Error Injection

A 429 will be injected mid-pagination (on page=2). You must wait for Retry-After, retry, and continue without losing pagination state.

## Instructions

**If you encounter bash syntax errors or escaping issues:**
- Fall back to simpler iterative commands rather than adding more complexity.
- One bcq command per action is always reliable, even if less efficient.

- **bcq**: For each project ID, run:
  1. `bcq todos --overdue --in <project_id> --json`
  2. Note which IDs have `title` starting with `Benchmark Overdue Todo`
  3. For each matching ID, issue TWO separate commands:
     - `bcq comment "Processed BenchChain $BCQ_BENCH_RUN_ID" --on <id> --in <project_id>`
     - `bcq done <id> --project <project_id>`
  4. Repeat for next project
- **raw API**:
  - For each project, get todoset from `/projects/<id>.json`
  - Paginate `/buckets/<id>/todosets/<todoset>/todolists.json?per_page=50`
  - Paginate `/buckets/<id>/todolists/<list>/todos.json?per_page=50`
  - Filter overdue as above (due_on < today AND completed == false)
  - POST comment with content `Processed BenchChain <run_id>` (use $BCQ_BENCH_RUN_ID)
  - POST completion for each match
  - Handle 429: read Retry-After header, sleep, retry same request

## Success

All benchmark overdue todos across both projects are commented once and completed.
