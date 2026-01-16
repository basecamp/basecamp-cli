#!/bin/bash

source env.sh

echo "============================================" >&2
echo "VERIFYING OVERDUE SWEEP RESULTS"
echo "============================================" >&2

BENCH_RUN="$BCQ_BENCH_RUN_ID"
echo "Searching for todos processed with run ID: $BENCH_RUN" >&2
echo ""

# Function to count processed todos in a project
count_processed() {
    local project_id=$1
    local project_name=$2
    local count=0
    
    echo "Checking $project_name (ID: $project_id)..." >&2
    
    # Get todoset
    local todoset_id=$(curl -s -X GET "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    [ -z "$todoset_id" ] && return 0
    
    # Get todolists
    local todolists=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.[].id')
    
    # Check all todos for completed ones with matching bench run in comments
    while read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        for page in {1..30}; do
            todos=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
            
            # Get completed overdue benchmark todos
            local todo_ids=$(echo "$todos" | jq -r '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == true) | .id')
            
            if [ -z "$(echo "$todos" | jq -r '.[] | .id' | head -1)" ]; then
                break
            fi
            
            while read -r todo_id; do
                [ -z "$todo_id" ] && continue
                
                # Check if this todo has a comment with our bench run ID
                local comments=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" \
                    -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
                
                if echo "$comments" | jq . 2>/dev/null | grep -q "$BENCH_RUN"; then
                    ((count++))
                fi
            done <<< "$todo_ids"
        done
    done <<< "$todolists"
    
    echo "  Processed: $count" >&2
    echo "$count"
}

p1=$(count_processed "$BCQ_BENCH_PROJECT_ID" "Project 1")
p2=$(count_processed "$BCQ_BENCH_PROJECT_ID_2" "Project 2")

TOTAL=$((p1 + p2))

echo ""
echo "============================================" >&2
echo "RESULTS"
echo "============================================" >&2
echo "Project 1: $p1 overdue todos processed" >&2
echo "Project 2: $p2 overdue todos processed" >&2
echo "TOTAL: $TOTAL overdue todos processed" >&2
echo "============================================" >&2

