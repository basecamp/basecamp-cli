#!/bin/bash
echo "Searching for Benchmark Overdue Todo items..."
for page in 1 2 3 4 5; do
  echo "=== Page $page ==="
  curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/1069480972/todos.json?page=$page" | \
    jq '[.[] | select(.title | startswith("Benchmark Overdue Todo")) | {id, title, due_on, completed}]'
done
