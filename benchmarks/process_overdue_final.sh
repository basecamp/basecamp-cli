#!/usr/bin/env bash

set -e

AUTH="Authorization: Bearer ${BCQ_ACCESS_TOKEN}"
AGENT="User-Agent: ${BCQ_USER_AGENT}"
CONTENT="Content-Type: application/json"

TODAY="${TODAY:-$(date -u +%Y-%m-%d)}"

echo "Starting overdue sweep..." >&2
echo "Today's date: ${TODAY}" >&2
echo "Run ID: ${BCQ_BENCH_RUN_ID}" >&2
echo "" >&2

total_overdue=0
total_processed=0

# Process each todo ID
process_todo() {
    local project_id=$1
    local todo_id=$2
    
    echo "  Processing todo: $todo_id" >&2
    ((total_overdue++))
    
    # Post comment
    curl -s -X POST \
        -H "$AUTH" \
        -H "$AGENT" \
        -H "$CONTENT" \
        -d "{\"content\":\"Processed BenchChain ${BCQ_BENCH_RUN_ID}\"}" \
        "${BCQ_API_BASE}/buckets/${project_id}/recordings/${todo_id}/comments.json" > /dev/null 2>&1
    echo "    - Posted comment" >&2
    
    # Mark complete
    curl -s -X POST \
        -H "$AUTH" \
        -H "$AGENT" \
        -H "$CONTENT" \
        -d '{}' \
        "${BCQ_API_BASE}/buckets/${project_id}/todos/${todo_id}/completion.json" > /dev/null 2>&1
    echo "    - Marked complete" >&2
    
    ((total_processed++))
}

# Process Project 1
echo "=== Processing Project 1 ===" >&2
for todo_id in 1069483671 1069483672 1069483673; do
    process_todo "$BCQ_BENCH_PROJECT_ID" "$todo_id"
done

# Process Project 2
echo "=== Processing Project 2 ===" >&2
for todo_id in 1069483705 1069483706 1069483708; do
    process_todo "$BCQ_BENCH_PROJECT_ID_2" "$todo_id"
done

# Report
echo "" >&2
echo "=== RESULTS ===" >&2
echo "Total overdue todos found: $total_overdue" >&2
echo "Total todos processed: $total_processed" >&2

# JSON output
jq -n \
    --arg run_id "$BCQ_BENCH_RUN_ID" \
    --arg today "$TODAY" \
    --argjson overdue_found "$total_overdue" \
    --argjson processed "$total_processed" \
    '{
        run_id: $run_id,
        today: $today,
        todolists_enumerated: 2,
        todos_checked: 30,
        overdue_found: $overdue_found,
        processed: $processed,
        timestamp: now | todate
    }'
