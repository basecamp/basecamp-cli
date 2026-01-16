#!/bin/bash

# Don't exit on error so we can see what's happening
# set -e

# Track statistics
total_processed=0
project1_count=0
project2_count=0

# Function to make API call with 429 retry
api_call() {
  local method="$1"
  local url="$2"
  local data="$3"
  
  while true; do
    if [ -n "$data" ]; then
      response=$(curl -s -w "\n%{http_code}" -X "$method" "$url" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$data")
    else
      response=$(curl -s -w "\n%{http_code}" -X "$method" "$url" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
    fi
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "429" ]; then
      echo "Rate limited, sleeping 2s..." >&2
      sleep 2
      continue
    fi
    
    echo "$body"
    return 0
  done
}

# Function to process a single project
process_project() {
  local project_id="$1"
  local project_name="$2"
  local count=0
  
  echo "Processing project $project_name ($project_id)..." >&2
  
  # Get project and extract todoset ID
  project_json=$(api_call GET "$BCQ_API_BASE/projects/$project_id.json")
  todoset_id=$(echo "$project_json" | jq -r '.dock[] | select(.name == "todoset") | .id')
  
  if [ -z "$todoset_id" ] || [ "$todoset_id" = "null" ]; then
    echo "No todoset found for project $project_id" >&2
    return 0
  fi
  
  echo "Found todoset: $todoset_id" >&2
  
  # Get all todolists
  todolists=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json")
  list_ids=$(echo "$todolists" | jq -r '.[].id')
  
  # Process each todolist
  for list_id in $list_ids; do
    echo "  Processing todolist $list_id..." >&2
    page=1
    
    # Paginate through todos
    while true; do
      echo "    Fetching page $page..." >&2
      todos_json=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page")
      
      # Check if page is empty
      num_todos=$(echo "$todos_json" | jq '. | length')
      echo "    Found $num_todos todos on page $page" >&2
      if [ "$num_todos" -eq 0 ]; then
        break
      fi
      
      # Filter and process overdue todos
      overdue_todos=$(echo "$todos_json" | jq -r --arg today "$TODAY" '
        .[] | 
        select(.title | startswith("Benchmark Overdue Todo")) |
        select(.due_on != null and .due_on < $today) |
        select(.completed == false) |
        .id
      ')
      
      if [ -n "$overdue_todos" ]; then
        echo "    Found overdue todos: $overdue_todos" >&2
      fi
      
      for todo_id in $overdue_todos; do
        echo "    Processing overdue todo: $todo_id" >&2
        
        # Post comment
        comment_data="{\"content\":\"Processed BenchChain $BCQ_BENCH_RUN_ID\"}"
        api_call POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null
        
        # Complete todo
        api_call POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "" > /dev/null
        
        count=$((count + 1))
        echo "    Completed todo $todo_id" >&2
      done
      
      page=$((page + 1))
    done
  done
  
  echo "$count"
}

# Process both projects
echo "Starting overdue sweep across both projects..." >&2
echo "Today's date: $TODAY" >&2
echo "Run ID: $BCQ_BENCH_RUN_ID" >&2
echo "" >&2

project1_count=$(process_project "$BCQ_BENCH_PROJECT_ID" "Project 1")
project2_count=$(process_project "$BCQ_BENCH_PROJECT_ID_2" "Project 2")

total_processed=$((project1_count + project2_count))

echo "" >&2
echo "=== SWEEP COMPLETE ===" >&2
echo "Project 1: $project1_count todos processed" >&2
echo "Project 2: $project2_count todos processed" >&2
echo "Total: $total_processed todos processed" >&2

# Output final counts to stdout for verification
echo "Project 1: $project1_count"
echo "Project 2: $project2_count"
echo "Total: $total_processed"
