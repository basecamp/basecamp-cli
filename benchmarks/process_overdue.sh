#!/bin/bash

# Process overdue todos using pre-identified IDs  
set -e

# Configuration
AUTH_HEADER="Authorization: Bearer ${BCQ_ACCESS_TOKEN}"
USER_AGENT_HEADER="User-Agent: ${BCQ_USER_AGENT}"
CONTENT_TYPE_HEADER="Content-Type: application/json"

# Set TODAY if not already set
TODAY="${TODAY:-$(date -u +%Y-%m-%d)}"

# Pre-identified overdue IDs from env
PROJECT_1_OVERDUE="${BCQ_BENCH_OVERDUE_IDS:-1069483671,1069483672,1069483673}"
PROJECT_2_OVERDUE="${BCQ_BENCH_OVERDUE_IDS_2:-1069483705,1069483706,1069483708}"

# Counters
total_overdue_found=0
total_processed=0

# Helper: make HTTP request with 429 retry logic
make_request() {
    local method=$1
    local url=$2
    local data=$3
    
    while true; do
        if [ -z "$data" ]; then
            response=$(curl -s -w "\n%{http_code}" -X "$method" \
                -H "$AUTH_HEADER" \
                -H "$USER_AGENT_HEADER" \
                "$url")
        else
            response=$(curl -s -w "\n%{http_code}" -X "$method" \
                -H "$AUTH_HEADER" \
                -H "$USER_AGENT_HEADER" \
                -H "$CONTENT_TYPE_HEADER" \
                -d "$data" \
                "$url")
        fi
        
        # Extract status code (last line) and body (everything else)
        http_code=$(echo "$response" | tail -n 1)
        body=$(echo "$response" | sed '$d')
        
        if [ "$http_code" = "429" ]; then
            echo "  [429] Rate limited, sleeping 2s..." >&2
            sleep 2
            continue
        fi
        
        echo "$body"
        return 0
    done
}

# Process overdue todos
process_overdue_ids() {
    local project_id=$1
    local project_name=$2
    local ids_str=$3
    
    echo "=== Processing overdue todos in $project_name ===" >&2
    
    # Convert comma-separated IDs to array
    IFS=',' read -ra ids <<< "$ids_str"
    
    for todo_id in "${ids[@]}"; do
        todo_id=$(echo "$todo_id" | xargs) # trim whitespace
        [ -z "$todo_id" ] && continue
        
        ((total_overdue_found++))
        echo "  Found overdue todo: $todo_id" >&2
        
        # Step 5a: Post comment
        local comment_data="{\"content\":\"Processed BenchChain ${BCQ_BENCH_RUN_ID}\"}"
        local comment_url="${BCQ_API_BASE}/buckets/${project_id}/recordings/${todo_id}/comments.json"
        
        make_request POST "$comment_url" "$comment_data" > /dev/null
        echo "    - Posted comment" >&2
        
        # Step 5b: Mark as complete
        local completion_url="${BCQ_API_BASE}/buckets/${project_id}/todos/${todo_id}/completion.json"
        make_request POST "$completion_url" "{}" > /dev/null
        echo "    - Marked complete" >&2
        
        ((total_processed++))
    done
}

# Main execution
echo "Starting overdue sweep..." >&2
echo "Today's date: ${TODAY}" >&2
echo "Run ID: ${BCQ_BENCH_RUN_ID}" >&2
echo ""

process_overdue_ids "$BCQ_BENCH_PROJECT_ID" "Project 1" "$PROJECT_1_OVERDUE"
process_overdue_ids "$BCQ_BENCH_PROJECT_ID_2" "Project 2" "$PROJECT_2_OVERDUE"

# Report results
echo "" >&2
echo "=== RESULTS ===" >&2
echo "Total overdue todos found: $total_overdue_found" >&2
echo "Total todos processed: $total_processed" >&2

# Output JSON summary
jq -n \
    --arg run_id "$BCQ_BENCH_RUN_ID" \
    --arg today "$TODAY" \
    --argjson overdue_found "$total_overdue_found" \
    --argjson processed "$total_processed" \
    '{
        run_id: $run_id,
        today: $today,
        overdue_found: $overdue_found,
        processed: $processed,
        timestamp: now | todate
    }'
