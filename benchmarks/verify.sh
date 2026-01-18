#!/bin/bash
echo "=== Verifying Project 1 ===" 
for todo_id in 1069483671 1069483672 1069483673; do
  echo "Todo $todo_id:"
  curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/todos/$todo_id.json" | \
    jq '{id, title, completed}'
  echo "Latest comment:"
  curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID/recordings/$todo_id/comments.json" | \
    jq -r '.[-1].content'
  echo ""
done

echo "=== Verifying Project 2 ==="
for todo_id in 1069483705 1069483706 1069483708; do
  echo "Todo $todo_id:"
  curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID_2/todos/$todo_id.json" | \
    jq '{id, title, completed}'
  echo "Latest comment:"
  curl -s -H "Authorization: Bearer $BASECAMP_TOKEN" \
    "$BCQ_API_BASE/buckets/$BCQ_BENCH_PROJECT_ID_2/recordings/$todo_id/comments.json" | \
    jq -r '.[-1].content'
  echo ""
done
