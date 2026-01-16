#!/bin/bash

source env.sh
TODAY=$(date +%Y-%m-%d)

echo "=== Final Overdue Sweep ==="
echo "Today: $TODAY"
echo "Run ID: $BCQ_BENCH_RUN_ID"
echo ""

TOTAL=0

for project_id in "$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2"; do
    count=0
    echo "Processing Project $project_id..."
    
    # Get todoset
    todoset=$(curl -s "$BCQ_API_BASE/projects/$project_id.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    # Get todolists
    lists=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset/todolists.json" \
        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null | jq -r '.[].id')
    
    while read -r list_id; do
        [ -z "$list_id" ] && continue
        
        page=1
        while [ $page -le 10 ]; do
            todos=$(curl -s "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page" \
                -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" 2>/dev/null)
            
            if [ -z "$(echo "$todos" | jq -r '.[] | .id' 2>/dev/null | head -1)" ]; then
                break
            fi
            
            # Process overdue, uncompleted todos
            echo "$todos" | jq -c '.[] | select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == false) | select(.due_on != null)' 2>/dev/null | while read -r todo; do
                due=$(echo "$todo" | jq -r '.due_on')
                if [[ "$due" < "$TODAY" ]]; then
                    id=$(echo "$todo" | jq -r '.id')
                    title=$(echo "$todo" | jq -r '.title')
                    
                    echo "  Processing: $id - $title"
                    
                    # Comment
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/recordings/$id/comments.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d "{\"content\":\"Processed BenchChain $BCQ_BENCH_RUN_ID\"}" > /dev/null 2>&1
                    
                    # Complete
                    curl -s -X POST "$BCQ_API_BASE/buckets/$project_id/todos/$id/completion.json" \
                        -H "Authorization: Bearer $BCQ_ACCESS_TOKEN" \
                        -H "Content-Type: application/json" \
                        -d '{}' > /dev/null 2>&1
                    
                    count=$((count + 1))
                fi
            done
            
            ((page++))
        done
    done <<< "$lists"
    
    echo "  Processed: $count overdue todos"
    TOTAL=$((TOTAL + count))
done

echo ""
echo "========================================="
echo "FINAL RESULTS"
echo "========================================="
echo "Total processed: $TOTAL"
echo "========================================="

