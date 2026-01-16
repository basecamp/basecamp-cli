#!/bin/bash

source env.sh

BENCH_RUN="$BCQ_BENCH_RUN_ID"
TOTAL=0

echo "Counting todos processed by this sweep..."
echo "Looking for comment: 'Processed BenchChain $BENCH_RUN'"
echo ""

for project_id in "$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2"; do
    project_count=0
    
    # Get todoset
    todoset_id=$(curl -s "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    # Get todolists
    todolists=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null | jq -r '.[].id' 2>/dev/null)
    
    # For each todolist, check pages
    while read -r todolist_id; do
        [ -z "$todolist_id" ] && continue
        
        page=1
        while [ $page -le 15 ]; do
            todos=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null)
            
            # Get count of todos on this page
            todo_count=$(echo "$todos" | jq 'length' 2>/dev/null || echo 0)
            if [ "$todo_count" -eq 0 ]; then
                break
            fi
            
            # For each completed overdue benchmark todo, check comments
            echo "$todos" | jq -r '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == true) | .id' 2>/dev/null | while read -r todo_id; do
                [ -z "$todo_id" ] && continue
                
                # Get comments for this todo
                comments=$(curl -s "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" \
                    -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null)
                
                # Check if our bench run is in any comment
                if echo "$comments" | jq '.[] | .content' 2>/dev/null | grep -q "$BENCH_RUN"; then
                    project_count=$((project_count + 1))
                fi
            done
            
            ((page++))
        done
    done <<< "$todolists"
    
    echo "Project $project_id: $project_count"
    TOTAL=$((TOTAL + project_count))
done

echo ""
echo "========================================="
echo "TOTAL PROCESSED: $TOTAL"
echo "========================================="

