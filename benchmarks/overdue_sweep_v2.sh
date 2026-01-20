#!/bin/bash

# Overdue sweep script for both benchmark projects
set -euo pipefail

# Load environment
source env.sh

# Counters
total_processed=0

# Function to handle 429 rate limiting
api_call() {
  local url="$1"
  local method="${2:-GET}"
  local data="${3:-}"
  
  while true; do
    if [ "$method" = "POST" ] && [ -n "$data" ]; then
      response=$(curl -s -w "\n%{http_code}" -X POST "$url" \
        -H "Authorization: Bearer $BASECAMP_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$data")
    else
      response=$(curl -s -w "\n%{http_code}" -X POST "$url" \
        -H "Authorization: Bearer $BASECAMP_TOKEN" \
        -H "Content-Type: application/json")
    fi
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "429" ]; then
      echo "Rate limited, sleeping 2s..." >&2
      sleep 2
      continue
    fi
    
    echo "$body"
    break
  done
}

# Process a single project
process_project() {
  local project_id="$1"
  local project_name="$2"
  
  echo "Processing project: $project_name (ID: $project_id)" >&2
  
  # 1. Get project to extract todoset ID
  project_data=$(curl -s "$BCQ_API_BASE/projects/$project_id.json" \
    -H "Authorization: Bearer $BASECAMP_TOKEN")
  todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')
  
  if [ -z "$todoset_id" ] || [ "$todoset_id" = "null" ]; then
    echo "No todoset found in project $project_id" >&2
    return
  fi
  
  echo "Found todoset: $todoset_id" >&2
  
  # 2. Get all todolists
  todolists=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
    -H "Authorization: Bearer $BASECAMP_TOKEN")
  todolist_ids=$(echo "$todolists" | jq -r '.[].id')
  
  if [ -z "$todolist_ids" ]; then
    echo "No todolists found" >&2
    return
  fi
  
  # 3. Process each todolist
  for list_id in $todolist_ids; do
    echo "Processing todolist: $list_id" >&2
    
    # Paginate through all todos
    page=1
    while true; do
      todos=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page" \
        -H "Authorization: Bearer $BASECAMP_TOKEN")
      
      # Check if empty
      if [ "$(echo "$todos" | jq '. | length')" = "0" ]; then
        break
      fi
      
      # Filter overdue todos: title starts with "Benchmark Overdue Todo", not completed, due_on < TODAY
      overdue=$(echo "$todos" | jq -r --arg today "$TODAY" '
        .[] | 
        select(.title | startswith("Benchmark Overdue Todo")) |
        select(.completed == false) |
        select(.due_on != null and .due_on < $today) |
        .id
      ')
      
      # Process each overdue todo
      for todo_id in $overdue; do
        echo "Found overdue todo: $todo_id" >&2
        
        # Post comment
        comment_data=$(jq -n --arg run_id "$BCQ_BENCH_RUN_ID" '{content: ("Processed BenchChain " + $run_id)}')
        api_call "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "POST" "$comment_data" > /dev/null
        
        # Complete the todo
        api_call "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "POST" "" > /dev/null
        
        ((total_processed++))
        echo "Completed todo: $todo_id" >&2
      done
      
      ((page++))
    done
  done
}

# Process both benchmark projects
process_project "$BCQ_BENCH_PROJECT_ID" "Benchmark Project 1"
process_project "$BCQ_BENCH_PROJECT_ID_2" "Benchmark Project 2"

# Report results
echo "=== SWEEP COMPLETE ===" >&2
echo "Total overdue todos processed: $total_processed" >&2
echo "$total_processed"
