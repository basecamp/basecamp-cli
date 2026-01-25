#!/bin/bash

# Get first project's todos to check
project_data=$(curl -s "$BCQ_API_BASE/projects/$BCQ_BENCH_PROJECT_ID.json" \
  -H "Authorization: Bearer $BASECAMP_TOKEN")

todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')
echo "Project 1 Todoset ID: $todoset_id"

todolists=$(curl -s "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todosets/$todoset_id/todolists.json" \
  -H "Authorization: Bearer $BASECAMP_TOKEN")

list_id=$(echo "$todolists" | jq -r '.[0].id')
echo "First Todolist ID: $list_id"

todos=$(curl -s "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/$list_id/todos.json?page=1" \
  -H "Authorization: Bearer $BASECAMP_TOKEN")

echo ""
echo "Sample todos from page 1:"
echo "$todos" | jq -r '.[] | select(.title | startswith("Benchmark")) | "\(.title) - Due: \(.due_on) - Completed: \(.completed)"' | head -10

echo ""
echo "Today: $TODAY"
echo ""
echo "Overdue todos matching criteria:"
echo "$todos" | jq --arg today "$TODAY" '
  .[] | 
  select(
    (.title | startswith("Benchmark Overdue Todo")) and
    .completed == false and
    .due_on != null and
    .due_on < $today
  ) | "\(.id): \(.title) - Due: \(.due_on)"
'

