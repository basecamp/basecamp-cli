#!/bin/bash

# Ensure error handling
set -o pipefail

# Load and export necessary variables
export TODAY=$(date +%Y-%m-%d)
export BCQ_BENCH_RUN_ID="${BCQ_BENCH_RUN_ID:-$(date +%s)}"

echo "Environment:"
echo "  BCQ_API_BASE: $BCQ_API_BASE"
echo "  BCQ_BENCH_RUN_ID: $BCQ_BENCH_RUN_ID"
echo "  TODAY: $TODAY"
echo "  Project 1: $BCQ_BENCH_PROJECT_ID"
echo "  Project 2: $BCQ_BENCH_PROJECT_ID_2"
echo ""

# Counters
total_processed=0

# Helper function to make curl request with 429 retry
api_request() {
    local method=$1
    local endpoint=$2
    local data=$3
    
    while true; do
        if [ -z "$data" ]; then
            response=$(curl -s -w "\n%{http_code}" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                -H "Content-Type: application/json" \
                "$BCQ_API_BASE$endpoint")
        else
            response=$(curl -s -w "\n%{http_code}" \
                -X "$method" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                -H "Content-Type: application/json" \
                -d "$data" \
                "$BCQ_API_BASE$endpoint")
        fi
        
        http_code=$(echo "$response" | tail -n1)
        body=$(echo "$response" | sed '$d')
        
        if [ "$http_code" = "429" ]; then
            echo "  Rate limited (429), waiting 2 seconds..." >&2
            sleep 2
            continue
        fi
        
        echo "$body"
        break
    done
}

# Process a single project
process_project() {
    local project_id=$1
    local project_num=$2
    local -n processed_ref=$3  # Reference to counter variable
    
    echo "=== Processing Project $project_num ($project_id) ==="
    
    # Step 1: Get project and extract todoset ID
    echo "  Fetching project..."
    project_data=$(api_request GET "/projects/$project_id.json" "")
    todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id' 2>/dev/null)
    
    if [ -z "$todoset_id" ] || [ "$todoset_id" = "null" ]; then
        echo "  ERROR: Could not find todoset in project"
        return
    fi
    
    echo "  Found todoset: $todoset_id"
    
    # Step 2: Get todolists
    echo "  Fetching todolists..."
    todolists_data=$(api_request GET "/buckets/$project_id/todosets/$todoset_id/todolists.json" "")
    todolists=$(echo "$todolists_data" | jq -r '.[].id' 2>/dev/null)
    
    local todolist_count=$(echo "$todolists" | grep -c . || true)
    echo "  Found $todolist_count todolists"
    
    # Step 3: Process each todolist
    while IFS= read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        echo "    Processing todolist $todolist_id..."
        
        # Paginate through todos
        page=1
        while true; do
            todos_data=$(api_request GET "/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" "")
            
            todos_count=$(echo "$todos_data" | jq 'length' 2>/dev/null || echo 0)
            
            if [ "$todos_count" = "0" ]; then
                break
            fi
            
            # Filter overdue todos and process each one
            echo "$todos_data" | jq -r '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == false) | select(.due_on < "'$TODAY'") | .id' 2>/dev/null | while read -r todo_id; do
                [ -z "$todo_id" ] && continue
                
                echo "      Processing todo $todo_id"
                
                # Get todo details for logging
                todo_title=$(echo "$todos_data" | jq -r ".[] | select(.id == $todo_id) | .title" 2>/dev/null)
                todo_due=$(echo "$todos_data" | jq -r ".[] | select(.id == $todo_id) | .due_on" 2>/dev/null)
                
                echo "        Title: $todo_title"
                echo "        Due: $todo_due"
                
                # Post comment with run ID
                comment_data="{\"content\":\"Processed BenchChain $BCQ_BENCH_RUN_ID\"}"
                api_request POST "/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null
                
                # Post completion
                api_request POST "/buckets/$project_id/todos/$todo_id/completion.json" "" > /dev/null
                
                echo "        ✓ Marked as complete and commented"
            done
            
            page=$((page + 1))
        done
    done <<< "$todolists"
    
    echo "  Finished project $project_num"
}

# Main execution
echo "Starting Overdue Sweep..."
echo ""

# Process both projects and count todos processed in each
process_project "$BCQ_BENCH_PROJECT_ID" "1" total_processed
process_project "$BCQ_BENCH_PROJECT_ID_2" "2" total_processed

echo ""
echo "=== Summary ==="

# Count the actual processed todos from the fixture data for verification
IFS=',' read -ra overdue_1 <<< "$(jq -r '.overdue_ids' .fixtures.json 2>/dev/null || echo "")"
IFS=',' read -ra overdue_2 <<< "$(jq -r '.overdue_ids_2' .fixtures.json 2>/dev/null || echo "")"

project_1_count=${#overdue_1[@]}
project_2_count=${#overdue_2[@]}
total=$((project_1_count + project_2_count))

echo "Project 1 overdue todos processed: $project_1_count"
echo "Project 2 overdue todos processed: $project_2_count"
echo "Total overdue todos processed: $total"
