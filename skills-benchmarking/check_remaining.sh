#!/bin/bash
echo "Checking for remaining overdue todos..."
echo "Project 1:"
count=0
for page in 1 2 3 4 5; do
  result=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/1069480972/todos.json?page=$page" | \
    jq -r --arg today "$TODAY" '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.due_on != null and .due_on < $today) | select(.completed == false) | .id')
  if [ -n "$result" ]; then
    echo "$result"
    count=$((count + $(echo "$result" | wc -l)))
  fi
done
echo "Total: $count"

echo ""
echo "Project 2:"
count=0
for page in 1 2 3 4 5; do
  result=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID_2/todolists/1069481128/todos.json?page=$page" | \
    jq -r --arg today "$TODAY" '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.due_on != null and .due_on < $today) | select(.completed == false) | .id')
  if [ -n "$result" ]; then
    echo "$result"
    count=$((count + $(echo "$result" | wc -l)))
  fi
done
echo "Total: $count"
