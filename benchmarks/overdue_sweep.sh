#!/usr/bin/env bash
set -euo pipefail

# Source environment
source env.sh

# Counters
TOTAL_PROCESSED=0

# Function to make curl request with 429 retry
curl_with_retry() {
  local url="$1"
  local method="${2:-GET}"
  local data="${3:-}"
  
  while true; do
    if [ -z "$data" ]; then
      response=$(curl -s -w "\n%{http_code}" -X "$method" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        -H "User-Agent: BenchmarkAgent" \
        "$url")
    else
      response=$(curl -s -w "\n%{http_code}" -X "$method" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        -H "User-Agent: BenchmarkAgent" \
        -d "$data" \
        "$url")
    fi
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "429" ]; then
      echo "  [429 Rate limit] Sleeping 2s..." >&2
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
  
  echo "=== Processing Project: $project_name (ID: $project_id) ===" >&2
  
  # 1. Get project and extract todoset ID
  project_data=$(curl_with_retry "$BCQ_API_BASE/projects/$project_id.json")
  todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')
  
  if [ -z "$todoset_id" ] || [ "$todoset_id" = "null" ]; then
    echo "  No todoset found for project $project_id" >&2
    return 0
  fi
  
  echo "  Todoset ID: $todoset_id" >&2
  
  # 2. Get all todolists
  todolists=$(curl_with_retry "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json")
  todolist_ids=$(echo "$todolists" | jq -r '.[].id')
  
  if [ -z "$todolist_ids" ]; then
    echo "  No todolists found" >&2
    return 0
  fi
  
  # 3. Process each todolist
  while IFS= read -r list_id; do
    [ -z "$list_id" ] && continue
    
    echo "  Processing todolist: $list_id" >&2
    
    # Paginate through todos
    page=1
    while true; do
      todos=$(curl_with_retry "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page")
      
      # Check if page is empty
      count=$(echo "$todos" | jq 'length')
      if [ "$count" -eq 0 ]; then
        break
      fi
      
      echo "    Page $page: $count todos" >&2
      
      # 4. Filter overdue todos
      overdue=$(echo "$todos" | jq -r --arg today "$TODAY" '
        .[] | 
        select(
          (.title | startswith("Benchmark Overdue Todo")) and
          .due_on != null and
          .due_on < $today and
          .completed == false
        ) | 
        .id
      ')
      
      # 5. Process each overdue todo
      if [ -n "$overdue" ]; then
        while IFS= read -r todo_id; do
          [ -z "$todo_id" ] && continue
          
          echo "    Processing overdue todo: $todo_id" >&2
          
          # Post comment
          comment_data=$(jq -n --arg content "Processed BenchChain $BCQ_BENCH_RUN_ID" '{content: $content}')
          curl_with_retry "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "POST" "$comment_data" > /dev/null
          
          # Complete todo
          curl_with_retry "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "POST" "" > /dev/null
          
          TOTAL_PROCESSED=$((TOTAL_PROCESSED + 1))
          echo "      ✓ Commented and completed todo $todo_id" >&2
        done <<< "$overdue"
      fi
      
      page=$((page + 1))
    done
  done <<< "$todolist_ids"
  
  echo "" >&2
}

# Process both projects
process_project "$BCQ_BENCH_PROJECT_ID" "Project 1"
process_project "$BCQ_BENCH_PROJECT_ID_2" "Project 2"

# Report results
echo "========================================" >&2
echo "SWEEP COMPLETE" >&2
echo "Total overdue todos processed: $TOTAL_PROCESSED" >&2
echo "Run ID: $BCQ_BENCH_RUN_ID" >&2
echo "========================================" >&2

# Output for verification
echo "$TOTAL_PROCESSED"
