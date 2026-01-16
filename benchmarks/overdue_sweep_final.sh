#!/opt/homebrew/bin/bash

# Load environment first
source env.sh

# Set TODAY if not already set
TODAY=${TODAY:-$(date +%Y-%m-%d)}
export TODAY

# Verify required variables
if [[ -z "$BASECAMP_TOKEN" || -z "$BCQ_API_BASE" || -z "$BCQ_BENCH_PROJECT_ID" || -z "$BCQ_BENCH_PROJECT_ID_2" || -z "$BCQ_BENCH_RUN_ID" ]]; then
    echo "Error: Required environment variables not set"
    exit 1
fi

# Helper function for API calls with retry on 429
api_call() {
    local method="$1"
    local url="$2"
    local data="${3:-}"
    local max_retries=5
    local retry=0
    
    while [ $retry -lt $max_retries ]; do
        if [ "$method" = "GET" ]; then
            response=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer $BASECAMP_TOKEN" "$url")
        else
            response=$(curl -s -w "\n%{http_code}" -X "$method" -H "Authorization: Bearer $BASECAMP_TOKEN" -H "Content-Type: application/json" -d "$data" "$url")
        fi
        
        http_code=$(echo "$response" | tail -n 1)
        body=$(echo "$response" | sed '$d')
        
        if [ "$http_code" = "429" ]; then
            echo "  Rate limited (429), sleeping 2s..." >&2
            ((retry++))
            sleep 2
            continue
        fi
        
        if [[ "$http_code" =~ ^2 ]]; then
            echo "$body"
            return 0
        else
            echo "Error: HTTP $http_code for $url" >&2
            echo "$body" >&2
            return 1
        fi
    done
    
    echo "Error: Max retries exceeded for $url" >&2
    return 1
}

# Process a single project
process_project() {
    local project_id="$1"
    local project_name="$2"
    local overdue_count=0
    
    echo "Processing project $project_name (ID: $project_id)..." >&2
    
    # Get project and extract todoset ID
    project_data=$(api_call GET "$BCQ_API_BASE/projects/$project_id.json")
    todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')
    
    if [ -z "$todoset_id" ] || [ "$todoset_id" = "null" ]; then
        echo "  No todoset found in project $project_name" >&2
        echo "0"
        return 0
    fi
    
    echo "  Found todoset: $todoset_id" >&2
    
    # Get all todolists
    todolists=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json")
    todolist_ids=$(echo "$todolists" | jq -r '.[].id')
    
    if [ -z "$todolist_ids" ]; then
        echo "  No todolists found" >&2
        echo "0"
        return 0
    fi
    
    # Process each todolist
    for list_id in $todolist_ids; do
        echo "  Processing todolist: $list_id" >&2
        page=1
        
        while true; do
            todos=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todolists/$list_id/todos.json?page=$page")
            
            # Check if page is empty
            todo_count=$(echo "$todos" | jq '. | length')
            if [ "$todo_count" = "0" ]; then
                echo "    Page $page: empty, stopping pagination" >&2
                break
            fi
            echo "    Page $page: $todo_count todos" >&2
            
            # Filter overdue todos
            overdue_todos=$(echo "$todos" | jq -r --arg today "$TODAY" '
                .[] | 
                select(.title | startswith("Benchmark Overdue Todo")) |
                select(.completed == false) |
                select(.due_on != null and .due_on < $today) |
                .id
            ')
            
            # Process each overdue todo
            for todo_id in $overdue_todos; do
                echo "    Found overdue todo: $todo_id" >&2
                
                # Add comment
                comment_data=$(jq -n --arg run_id "$BCQ_BENCH_RUN_ID" '{content: "Processed BenchChain \($run_id)"}')
                api_call POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null
                echo "      Added comment" >&2
                
                # Complete todo
                api_call POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "{}" > /dev/null
                echo "      Completed todo" >&2
                
                ((overdue_count++))
            done
            
            ((page++))
        done
    done
    
    echo "  Project $project_name: Processed $overdue_count overdue todos" >&2
    echo "$overdue_count"
}

# Main execution
echo "=== Starting Overdue Sweep ==="
echo "Today's date: $TODAY"
echo "Run ID: $BCQ_BENCH_RUN_ID"
echo ""

count1=$(process_project "$BCQ_BENCH_PROJECT_ID" "Project 1")
echo ""
count2=$(process_project "$BCQ_BENCH_PROJECT_ID_2" "Project 2")

total=$((count1 + count2))
echo ""
echo "=== Sweep Complete ==="
echo "Project 1: $count1 overdue todos processed"
echo "Project 2: $count2 overdue todos processed"
echo "Total: $total overdue todos processed"
