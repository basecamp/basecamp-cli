#!/bin/bash
echo "Checking all pages for Overdue todos in project 1:"
for page in 1 2 3 4 5 6 7 8; do
  result=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/1069480972/todos.json?page=$page" | \
    jq '[.[] | select(.title | startswith("Benchmark Overdue"))] | length')
  
  if [ "$result" != "0" ]; then
    echo "Page $page: $result todos"
    curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
      "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/1069480972/todos.json?page=$page" | \
      jq '[.[] | select(.title | startswith("Benchmark Overdue")) | {id, title, due_on, completed}]'
  fi
done
