#!/opt/homebrew/bin/bash
set -e

# Fetch overdue todos from both projects and complete them
projects=("$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2")
completed_count=0

for project_id in "${projects[@]}"; do
  todolists=$(curl -s -H "Authorization: Bearer $BCQ_TOKEN" \
    "https://3.basecampapi.com/$BCQ_ACCOUNT_ID/buckets/$project_id/todolists.json")
  
  todolist_ids=$(echo "$todolists" | jq -r '.[].id')
  
  for list_id in $todolist_ids; do
    todos=$(curl -s -H "Authorization: Bearer $BCQ_TOKEN" \
      "https://3.basecampapi.com/$BCQ_ACCOUNT_ID/buckets/$project_id/todolists/$list_id.json" | \
      jq -r '.todos[] | select(.completed == false and .due_on != null) | @json')
    
    while IFS= read -r todo; do
      [ -z "$todo" ] && continue
      
      todo_id=$(echo "$todo" | jq -r '.id')
      due_on=$(echo "$todo" | jq -r '.due_on')
      
      # Check if overdue
      due_epoch=$(date -j -f "%Y-%m-%d" "$due_on" +%s 2>/dev/null || echo "0")
      today_epoch=$(date +%s)
      
      if [ "$due_epoch" -lt "$today_epoch" ] && [ "$due_epoch" != "0" ]; then
        curl -s -X POST \
          -H "Authorization: Bearer $BCQ_TOKEN" \
          -H "Content-Type: application/json" \
          "https://3.basecampapi.com/$BCQ_ACCOUNT_ID/buckets/$project_id/todos/$todo_id/completion.json" \
          -d "{\"comment\": \"Overdue sweep: $BCQ_BENCH_RUN_ID\"}" > /dev/null
        ((completed_count++))
      fi
    done <<< "$todos"
  done
done

echo "{\"swept_count\": $completed_count, \"completed_count\": $completed_count}"
