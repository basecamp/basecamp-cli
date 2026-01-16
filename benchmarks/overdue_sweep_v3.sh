#!/bin/bash

source env.sh
TODAY=$(date +%Y-%m-%d)

TOTAL_PROCESSED=0

process_project() {
    local project_id=$1
    local project_name=$2
    local count=0
    
    echo "=== Processing $project_name ===" >&2
    
    # Get todoset ID
    local todoset_id=$(curl -s -X GET "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    [ -z "$todoset_id" ] && { echo "Error: No todoset"; return; }
    echo "Todoset: $todoset_id" >&2
    
    # Get todolists
    local todolists=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.[].id')
    
    # For each todolist
    while IFS= read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        local page=1
        while true; do
            local todos=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
            
            # Check if empty
            local todo_count=$(echo "$todos" | jq 'length')
            if [ "$todo_count" -eq 0 ]; then
                break
            fi
            
            # Build list of todos to process  
            local todo_ids=$(echo "$todos" | jq -r '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == false) | select(.due_on != null) | .id')
            
            # Process each todo
            while IFS= read -r todo_id; do
                [ -z "$todo_id" ] && continue
                
                local todo_data=$(echo "$todos" | jq ".[] | select(.id == $todo_id)")
                local due_on=$(echo "$todo_data" | jq -r '.due_on')
                local title=$(echo "$todo_data" | jq -r '.title')
                
                # Check if overdue
                if [[ "$due_on" < "$TODAY" ]]; then
                    echo "Found: $title (due $due_on)" >&2
                    
                    # Post comment
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d "{\"content\":\"Processed BenchChain $BCQ_BENCH_RUN_ID\"}" > /dev/null
                    
                    # Mark complete
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d '{}' > /dev/null
                    
                    ((count++))
                    echo "Processed: ID $todo_id" >&2
                fi
            done <<< "$todo_ids"
            
            ((page++))
        done
    done <<< "$todolists"
    
    echo "$project_name: $count processed" >&2
    TOTAL_PROCESSED=$((TOTAL_PROCESSED + count))
}

echo "Overdue Sweep Starting" >&2
echo "Today: $TODAY" >&2
echo "Run ID: $BCQ_BENCH_RUN_ID" >&2

process_project "$BCQ_BENCH_PROJECT_ID" "Project 1"
process_project "$BCQ_BENCH_PROJECT_ID_2" "Project 2"

echo ""
echo "=============================="
echo "TOTAL PROCESSED: $TOTAL_PROCESSED"
echo "=============================="

