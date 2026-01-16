#!/bin/bash

set -e

# Source environment
source env.sh

# Initialize counters
TOTAL_TODOLISTS=0
TOTAL_OVERDUE=0
TOTAL_PROCESSED=0

echo "=== Overdue Sweep Across Benchmark Projects ==="
echo "Access Token: ${BCQ_ACCESS_TOKEN:0:20}..."
echo "API Base: $BCQ_API_BASE"
echo "Project 1: $BCQ_BENCH_PROJECT_ID"
echo "Project 2: $BCQ_BENCH_PROJECT_ID_2"
echo "Today's Date: $TODAY"
echo "Bench Run ID: $BCQ_BENCH_RUN_ID"
echo ""

# Function to handle HTTP requests with 429 retry logic
function make_request() {
  local method=$1
  local url=$2
  local data="${3:-}"
  local max_retries=3
  local retry_count=0
  
  while true; do
    if [ -z "$data" ]; then
      response=$(curl -s -w "\n%{http_code}" -X "$method" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        "$url")
    else
      response=$(curl -s -w "\n%{http_code}" -X "$method" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$data" \
        "$url")
    fi
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "429" ]; then
      retry_count=$((retry_count + 1))
      if [ $retry_count -le $max_retries ]; then
        echo "  [429] Rate limited. Sleeping 2s before retry ($retry_count/$max_retries)..."
        sleep 2
        continue
      fi
    fi
    
    echo "$body"
    return 0
  done
}

# Function to process a single project
function process_project() {
  local project_id=$1
  local project_name=$2
  
  echo "=== Processing $project_name (ID: $project_id) ==="
  
  # Get project details to extract todoset ID
  echo "Fetching project details..."
  project=$(make_request GET "$BCQ_API_BASE/projects/$project_id.json")
  
  # Extract todoset ID from dock - looking for name == "todoset"
  todoset_id=$(echo "$project" | jq -r '.dock[] | select(.name == "todoset") | .id // empty')
  
  if [ -z "$todoset_id" ]; then
    echo "  ✗ Could not find todoset in project dock"
    return
  fi
  
  echo "  ✓ Found todoset: $todoset_id"
  
  # Get all todolists from the todoset
  echo "Fetching todolists from todoset..."
  todolists=$(make_request GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json")
  
  todolist_ids=$(echo "$todolists" | jq -r '.[].id')
  todolist_count=$(echo "$todolist_ids" | grep -c . || echo 0)
  
  echo "  ✓ Found $todolist_count todolists"
  TOTAL_TODOLISTS=$((TOTAL_TODOLISTS + todolist_count))
  
  # Process each todolist
  while IFS= read -r list_id; do
    [ -z "$list_id" ] && continue
    
    echo "  Processing todolist: $list_id"
    
    # Paginate through todos
    page=1
    while true; do
      todos=$(make_request GET "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page")
      
      # Check if we got any todos
      todo_count=$(echo "$todos" | jq 'length')
      if [ "$todo_count" -eq 0 ]; then
        break
      fi
      
      # Filter overdue todos: title starts with "Benchmark Overdue Todo", due_on < TODAY, completed == false
      overdue_todos=$(echo "$todos" | jq -r ".[] | select(.title | startswith(\"Benchmark Overdue Todo\")) | select(.completed == false) | select(.due_on < \"$TODAY\") | .id")
      
      while IFS= read -r todo_id; do
        [ -z "$todo_id" ] && continue
        
        TOTAL_OVERDUE=$((TOTAL_OVERDUE + 1))
        
        # Extract todo details for logging
        todo_data=$(echo "$todos" | jq ".[] | select(.id == $todo_id)")
        todo_title=$(echo "$todo_data" | jq -r '.title')
        todo_due=$(echo "$todo_data" | jq -r '.due_on')
        
        echo "    Found overdue: $todo_id ($todo_title, due: $todo_due)"
        
        # Post comment with expanded run ID
        comment_data=$(jq -n --arg msg "Processed BenchChain $BCQ_BENCH_RUN_ID" '{content: $msg}')
        echo "    Posting comment..."
        make_request POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null
        
        # Mark as complete
        echo "    Marking as complete..."
        make_request POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "" > /dev/null
        
        TOTAL_PROCESSED=$((TOTAL_PROCESSED + 1))
        echo "    ✓ Processed successfully"
        
      done <<< "$overdue_todos"
      
      page=$((page + 1))
    done
  done <<< "$todolist_ids"
  
  echo ""
}

# Process both projects
process_project "$BCQ_BENCH_PROJECT_ID" "Benchmark Project 1"
process_project "$BCQ_BENCH_PROJECT_ID_2" "Benchmark Project 2"

# Report final counts
echo "=== Summary ==="
echo "Total Todolists: $TOTAL_TODOLISTS"
echo "Total Overdue Todos Found: $TOTAL_OVERDUE"
echo "Total Processed: $TOTAL_PROCESSED"
echo "✓ Sweep complete"
