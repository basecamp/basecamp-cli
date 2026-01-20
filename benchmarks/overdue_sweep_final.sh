#!/bin/bash

set -e
source env.sh

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
        
        todos=$(echo "$response" | jq -c '.[]' 2>/dev/null || echo "")
        
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

# Process both projects in a single pass
process_all_projects() {
    local project1=$BCQ_BENCH_PROJECT_ID
    local project2=$BCQ_BENCH_PROJECT_ID_2
    local count=0

    for project_id in "$project1" "$project2"; do
        echo "======================================== " >&2
        echo "Processing Project: $project_id" >&2
        echo "======================================== " >&2

        # Get project details to extract todoset ID
        echo "Fetching project details..." >&2
        project_data=$(api_call GET "$BCQ_API_BASE/projects/$project_id.json" "")
        todoset_id=$(echo "$project_data" | jq -r '.dock[] | select(.name == "todoset") | .id')

        if [ -z "$todoset_id" ]; then
            echo "No todoset found for project $project_id" >&2
            continue
        fi

        echo "Found todoset ID: $todoset_id" >&2

        # Get all todolists in the todoset
        echo "Fetching todolists..." >&2
        todolists=$(api_call GET "$BCQ_API_BASE/buckets/$project_id/todosets/$todoset_id/todolists.json" "")
        
        # Process each todolist
        while IFS= read -r todolist; do
            if [ -z "$todolist" ]; then
                continue
            fi

            todolist_id=$(echo "$todolist" | jq -r '.id')
            todolist_name=$(echo "$todolist" | jq -r '.name')
            
            echo "Processing todolist: $todolist_name (ID: $todolist_id)" >&2

            # Get all todos for this todolist (with pagination)
            todos=$(get_all_todos "$project_id" "$todolist_id")

            # Filter overdue todos that match criteria and process each
            while IFS= read -r todo; do
                if [ -z "$todo" ]; then
                    continue
                fi

                # Extract fields
                title=$(echo "$todo" | jq -r '.title // ""')
                completed=$(echo "$todo" | jq -r '.completed')
                due_on=$(echo "$todo" | jq -r '.due_on // ""')
                todo_id=$(echo "$todo" | jq -r '.id')
                
                # Check all conditions for matching
                if [[ ! "$title" =~ ^Benchmark\ Overdue\ Todo ]]; then
                    continue
                fi
                
                if [ "$completed" != "false" ]; then
                    continue
                fi
                
                # Check if due_on exists and is before TODAY using string comparison
                if [ -z "$due_on" ]; then
                    continue
                fi
                
                if [[ "$due_on" -ge "$TODAY" ]]; then
                    continue
                fi

                # This is an overdue todo that matches criteria
                echo "Found overdue todo: $title (ID: $todo_id, Due: $due_on)" >&2

                # Add comment with run ID (expanded variable in double quotes)
                comment_content="Processed BenchChain $BCQ_BENCH_RUN_ID"
                echo "Adding comment: $comment_content" >&2
                comment_data=$(jq -n --arg content "$comment_content" '{content: $content}')
                api_call POST "$BCQ_API_BASE/buckets/$project_id/recordings/$todo_id/comments.json" "$comment_data" > /dev/null 2>&1

                # Mark as completed
                echo "Marking todo as completed..." >&2
                api_call POST "$BCQ_API_BASE/buckets/$project_id/todos/$todo_id/completion.json" "" > /dev/null 2>&1

                count=$((count + 1))
            done < <(echo "$todos" | jq -c '.')
        done < <(echo "$todolists" | jq -c '.[]')
    done

    echo "" >&2
    echo "========================================" >&2
    echo "SWEEP COMPLETE" >&2
    echo "========================================" >&2
    echo "Total todos processed: $count" >&2
    echo "Total comments added: $count" >&2
    echo "Total todos completed: $count" >&2
    echo "========================================" >&2
}

process_all_projects

