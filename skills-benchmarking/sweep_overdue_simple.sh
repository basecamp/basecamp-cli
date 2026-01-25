#!/bin/bash
source env.sh

TODAY=$(date +%Y-%m-%d)
echo "Today: $TODAY, Run ID: $BCQ_BENCH_RUN_ID"

processed=0
completed=0

# Function to process todo (avoids subshell issue)
process_todo() {
    local proj=$1
    local todo_id=$2
    local title=$3
    
    echo "  Processing: $title ($todo_id)"
    
    # Comment
    curl -s -X POST \
      -H "Authorization: Bearer $BASECAMP_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"content\":\"Processed BenchChain $BCQ_BENCH_RUN_ID\"}" \
      "$BCQ_API_BASE/buckets/$proj/recordings/$todo_id/comments.json" > /dev/null
    
    # Complete
    curl -s -X POST \
      -H "Authorization: Bearer $BASECAMP_TOKEN" \
      "$BCQ_API_BASE/buckets/$proj/todos/$todo_id/completion.json" > /dev/null
    
    echo "  Completed!"
}

# Explicit list of project IDs to process
projects=("$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2")

for proj in "${projects[@]}"; do
    echo ""
    echo "[Project $proj] Starting..."
    
    # Get todoset
    project_json=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" "$BCQ_API_BASE/projects/$proj.json")
    todoset=$(echo "$project_json" | jq -r '.dock[] | select(.name=="todoset") | .id')
    echo "[Project $proj] Todoset: $todoset"
    
    # Get todolists
    lists_json=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" "$BCQ_API_BASE/buckets/$proj/todosets/$todoset/todolists.json")
    lists=$(echo "$lists_json" | jq -r '.[] | .id')
    
    for list in $lists; do
        echo "[Project $proj][List $list] Processing..."
        
        page=1
        while true; do
            todos=$(curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" "$BCQ_API_BASE/buckets/$proj/todolists/$list/todos.json?page=$page")
            count=$(echo "$todos" | jq 'length')
            [ "$count" -eq 0 ] && break
            
            echo "[Project $proj][List $list][Page $page] Found $count todos"
            
            # Save matching todos to temp file and process them
            matches=$(mktemp)
            echo "$todos" | jq -c ".[] | select(.title | startswith(\"Benchmark Overdue Todo\")) | select(.due_on != null and .due_on < \"$TODAY\") | select(.completed == false)" > "$matches"
            
            while IFS= read -r todo_line; do
                [ -z "$todo_line" ] && continue
                todo_id=$(echo "$todo_line" | jq -r '.id')
                title=$(echo "$todo_line" | jq -r '.title')
                process_todo "$proj" "$todo_id" "$title"
                ((processed++))
                ((completed++))
            done < "$matches"
            
            rm -f "$matches"
            ((page++))
        done
    done
done

echo ""
echo "========================================"
echo "Complete. Processed: $processed"
echo "========================================"
