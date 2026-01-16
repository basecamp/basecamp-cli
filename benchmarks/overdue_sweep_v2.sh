#!/bin/bash

source env.sh
TODAY=$(date +%Y-%m-%d)

TOTAL_PROCESSED=0

process_project() {
    local project_id=$1
    local project_name=$2
    local count=0
    
    echo "Processing $project_name (ID: $project_id)..." >&2
    
    # Get todoset ID
    local todoset_id=$(curl -s -X GET "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    [ -z "$todoset_id" ] && { echo "Error: No todoset found"; return; }
    
    # Get todolists
    local todolists=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" | jq -r '.[].id')
    
    # For each todolist, get all todos across all pages
    while read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        local page=1
        while true; do
            # Get todos for this page
            local todos=$(curl -s -X GET "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN")
            
            # Check if empty
            [ -z "$(echo "$todos" | jq -r '.[] | .id' | head -1)" ] && break
            
            # Process each todo that matches criteria
            echo "$todos" | jq -c '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == false) | select(.due_on != null)' | while read -r todo_line; do
                local todo_id=$(echo "$todo_line" | jq -r '.id')
                local due_on=$(echo "$todo_line" | jq -r '.due_on')
                local title=$(echo "$todo_line" | jq -r '.title')
                
                # Check if overdue (lexicographic comparison works for YYYY-MM-DD)
                if [[ "$due_on" < "$TODAY" ]]; then
                    echo "  Processing: $title (due: $due_on)" >&2
                    
                    # Post comment
                    local comment="Processed BenchChain $BCQ_BENCH_RUN_ID"
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d "{\"content\":\"$comment\"}" > /dev/null
                    
                    # Mark complete
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d '{}' > /dev/null
                    
                    ((count++))
                fi
            done
            
            ((page++))
        done
    done < <(echo "$todolists")
    
    echo "Processed $count overdue todos in $project_name" >&2
    TOTAL_PROCESSED=$((TOTAL_PROCESSED + count))
}

echo "Starting overdue sweep..." >&2
echo "Today: $TODAY, Run ID: $BCQ_BENCH_RUN_ID" >&2

process_project "$BCQ_BENCH_PROJECT_ID" "Project 1"
process_project "$BCQ_BENCH_PROJECT_ID_2" "Project 2"

echo ""
echo "========================================" >&2
echo "TOTAL PROCESSED: $TOTAL_PROCESSED" >&2
echo "========================================" >&2

