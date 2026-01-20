#!/bin/bash

set -e

# Source environment
source env.sh

# Initialize temp file for results
results_file=$(mktemp)
echo "0" > "$results_file"

# Helper function to handle 429 retries
api_call() {
    local method=$1
    local endpoint=$2
    local data=$3
    local max_retries=5
    local retry_count=0

    while [ $retry_count -lt $max_retries ]; do
        if [ -z "$data" ]; then
            response=$(curl -s -w "\n%{http_code}" -X "$method" "$endpoint" \
                -H "Authorization: Bearer $BASECAMP_TOKEN" \
                -H "Content-Type: application/json" \
                -H "User-Agent: BenchmarkTask (benchmark@test.com)")
        else
            response=$(curl -s -w "\n%{http_code}" -X "$method" "$endpoint" \
                -H "Authorization: Bearer $BASECAMP_TOKEN" \
                -H "Content-Type: application/json" \
                -H "User-Agent: BenchmarkTask (benchmark@test.com)" \
                -d "$data")
        fi

        http_code=$(echo "$response" | tail -n1)
        body=$(echo "$response" | sed '$d')

        if [ "$http_code" = "429" ]; then
            echo "Rate limited, sleeping 2 seconds..." >&2
            sleep 2
            retry_count=$((retry_count + 1))
            continue
        fi

        echo "$body"
        return 0
    done

    echo "Max retries exceeded for $endpoint" >&2
    return 1
}

# Helper function to paginate through todos
get_all_todos() {
    local project_id=$1
    local todolist_id=$2
    local page=1
    local all_todos=""

    while true; do
        echo "Fetching page $page of todos for todolist $todolist_id..." >&2
        response=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todolists/$todolist_id/todos.json?page=$page" "")
        
        todos=$(echo "$response" | jq -c '.[]')
        
        if [ -z "$todos" ]; then
            break
        fi

        if [ -z "$all_todos" ]; then
            all_todos="$todos"
        else
            all_todos=$(echo -e "$all_todos\n$todos")
        fi

        page=$((page + 1))
    done

    echo "$all_todos"
}

# Function to process a single project
process_project() {
    local project_id=$1
    local project_name=$2
    local processed=0

    echo "========================================" >&2
    echo "Processing $project_name (ID: $project_id)" >&2
    echo "========================================" >&2

    # Get project details to extract todoset ID
    echo "Fetching project details..." >&2
    project_data=$(api_call GET "$BCQ_API_BASE/projects/$project_id.json" "")
    todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')

    if [ -z "$todoset_id" ]; then
        echo "No todoset found for $project_name" >&2
        return 0
    fi

    echo "Found todoset ID: $todoset_id" >&2

    # Get all todolists in the todoset
    echo "Fetching todolists..." >&2
    todolists=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" "")
    
    # Process each todolist
    echo "$todolists" | jq -c '.[]' | while read -r todolist; do
        todolist_id=$(echo "$todolist" | jq -r '.id')
        todolist_name=$(echo "$todolist" | jq -r '.name')
        
        echo "Processing todolist: $todolist_name (ID: $todolist_id)" >&2

        # Get all todos for this todolist (with pagination)
        todos=$(get_all_todos "$project_id" "$todolist_id")

        # Filter overdue todos that match criteria
        echo "$todos" | jq -c 'select(.title | startswith("Benchmark Overdue Todo")) | select(.completed == false) | select(.due_on < "'$TODAY'")' | while read -r todo; do
            if [ -z "$todo" ]; then
                continue
            fi

            todo_id=$(echo "$todo" | jq -r '.id')
            todo_title=$(echo "$todo" | jq -r '.title')
            todo_due=$(echo "$todo" | jq -r '.due_on')

            echo "Found overdue todo: $todo_title (ID: $todo_id, Due: $todo_due)" >&2

            # Add comment with run ID
            comment_content="Processed BenchChain $BCQ_BENCH_RUN_ID"
            echo "Adding comment: $comment_content" >&2
            comment_data=$(jq -n --arg content "$comment_content" '{content: $content}')
            api_call POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null

            # Mark as completed
            echo "Marking todo as completed..." >&2
            api_call POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "" > /dev/null

            echo "Processed 1 todo" >&2
        done
    done
}

# Process both projects and collect results
process_project "$BCQ_BENCH_PROJECT_ID" "Benchmark Project 1" 2>&1 | tee /tmp/project1.log
process_project "$BCQ_BENCH_PROJECT_ID_2" "Benchmark Project 2" 2>&1 | tee /tmp/project2.log

# Count results
project1_count=$(grep -c "Found overdue todo:" /tmp/project1.log || echo 0)
project2_count=$(grep -c "Found overdue todo:" /tmp/project2.log || echo 0)
total_count=$((project1_count + project2_count))

# Report results
echo "" >&2
echo "========================================" >&2
echo "SWEEP COMPLETE" >&2
echo "========================================" >&2
echo "Project 1 todos processed: $project1_count" >&2
echo "Project 2 todos processed: $project2_count" >&2
echo "Total todos processed: $total_count" >&2
echo "Total comments added: $total_count" >&2
echo "Total todos completed: $total_count" >&2
echo "========================================" >&2

# Cleanup
rm "$results_file" /tmp/project1.log /tmp/project2.log 2>/dev/null || true

