# Task 12: Overdue Sweep Across Benchmark Projects

## Task

Find and process overdue todos across two benchmark projects using raw curl + jq.

## Efficiency Anchor

Efficient solutions exist that finish in **~1–2 bash calls** and a modest number of HTTP requests
by writing a single script that handles pagination and both projects in one pass.

**Correctness beats speed**: you must still enumerate todolists, paginate all pages, handle 429s,
and process both projects.

After sourcing env.sh, these variables are available:
- `BASECAMP_TOKEN` - Bearer token for Authorization header
- `BCQ_API_BASE` - Full API base URL with account (e.g., http://3.basecampapi.localhost:3001/181900405)
- `BCQ_BENCH_PROJECT_ID` - Project 1 ID
- `BCQ_BENCH_PROJECT_ID_2` - Project 2 ID
- `BCQ_BENCH_RUN_ID` - Run identifier (MUST include in comments)
- `TODAY` - Today's date in YYYY-MM-DD format

## API Workflow

For EACH project:
1. GET project → extract todoset ID from `.dock[]`
2. GET todolists from todoset
3. For each todolist, paginate through todos (page=1,2,... until empty)
4. Filter: `title` starts with "Benchmark Overdue Todo", `due_on < $TODAY`, `completed == false`
5. For each match: POST comment with "Processed BenchChain $BCQ_BENCH_RUN_ID", then POST completion

## Endpoints

```
GET  $BCQ_API_BASE/projects/<project_id>.json
GET  $BCQ_API_BASE/buckets/<project>/todosets/<todoset>/todolists.json
GET  $BCQ_API_BASE/buckets/<project>/todolists/<list>/todos.json?page=N
POST $BCQ_API_BASE/buckets/<project>/recordings/<todo>/comments.json  {"content":"..."}
POST $BCQ_API_BASE/buckets/<project>/todos/<todo>/completion.json
```

## Error Handling

On HTTP 429: sleep 2 seconds, retry same request.

## Requirements

- Comment MUST include the **expanded value** of `$BCQ_BENCH_RUN_ID` (use double quotes in bash)
- Both projects must be swept
- Report counts when done
