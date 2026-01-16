#!/bin/bash

echo "Searching for 'Benchmark Overdue Todo' items across all pages..."
echo "Today: $TODAY"
echo ""

project_data=$(curl -s "$BCQ_API_BASE/projects/$BCQ_BENCH_PROJECT_ID.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")

todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')

todolists=$(curl -s "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todosets/$todoset_id/todolists.json" \
  -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")

list_id=$(echo "$todolists" | jq -r '.[0].id')

echo "Checking all pages of todolist $list_id..."
page=1
found_any=false

while true; do
  todos=$(curl -s "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todolists/$list_id/todos.json?page=$page" \
    -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
  
  if [[ "$todos" == "[]" || -z "$todos" ]]; then
    break
  fi
  
  # Look for any "Benchmark Overdue Todo" items
  overdue=$(echo "$todos" | jq --arg today "$TODAY" '
    .[] | 
    select(.title | startswith("Benchmark Overdue Todo"))
  ')
  
  if [[ -n "$overdue" ]]; then
    echo ""
    echo "Page $page - Found 'Benchmark Overdue Todo' items:"
    echo "$overdue" | jq -r '"\(.id): \(.title) - Due: \(.due_on) - Completed: \(.completed)"'
    found_any=true
  fi
  
  ((page++))
done

if [[ "$found_any" == "false" ]]; then
  echo "No 'Benchmark Overdue Todo' items found in any page"
fi

