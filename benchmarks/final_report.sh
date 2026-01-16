#!/bin/bash

source env.sh

echo ""
echo "========================================="
echo "FINAL OVERDUE SWEEP REPORT"
echo "========================================="
echo "Run ID: $BCQ_BENCH_RUN_ID"
echo "Sweep Date: $(date)"
echo ""

count_processed_todos() {
    local project_id=$1
    local project_name=$2
    local count=0
    
    # Get todoset
    local todoset_id=$(curl -s -X GET "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    [ -z "$todoset_id" ] && { echo "$project_name: ERROR - no todoset found"; echo "0"; return 1; }
    
    # Get todolists
    local todolists=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.[].id')
    
    # Scan all todos
    while read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        for page in 1 2 3 4 5 6 7 8 9 10; do
            local todos=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null)
            
            # Check if we have any todos
            if [ -z "$(echo "$todos" | jq -r '.[] | .id' 2>/dev/null | head -1)" ]; then
                break
            fi
            
            # Count completed overdue benchmark todos on this page
            local page_count=$(echo "$todos" | jq '[.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == true)] | length' 2>/dev/null || echo 0)
            count=$((count + page_count))
        done
    done <<< "$todolists"
    
    echo "$project_name: $count completed overdue todos"
    echo "$count"
}

echo "Scanning Project 1..."
result1=$(count_processed_todos "$BCQ_BENCH_PROJECT_ID" "Project 1")
p1=$(echo "$result1" | tail -n 1)

echo "Scanning Project 2..."
result2=$(count_processed_todos "$BCQ_BENCH_PROJECT_ID_2" "Project 2")
p2=$(echo "$result2" | tail -n 1)

total=$((p1 + p2))

echo ""
echo "========================================="
echo "SUMMARY"
echo "========================================="
echo "$result1"
echo "$result2"
echo ""
echo "TOTAL OVERDUE TODOS PROCESSED: $total"
echo "========================================="
echo ""

