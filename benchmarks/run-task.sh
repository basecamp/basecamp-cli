#!/usr/bin/env bash
# Non-interactive task runner for benchmark automation
# Usage: ./run-task.sh --task 12 --condition bcq-default

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$BENCH_DIR"

TASK=""
CONDITION="bcq-default"
MATCH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --task|-t) TASK="$2"; shift 2 ;;
    --condition|-c) CONDITION="$2"; shift 2 ;;
    --match|-m) MATCH="$2"; shift 2 ;;
    *) shift ;;
  esac
done

[[ -z "$TASK" ]] && { echo "Usage: $0 --task <id> --condition <cond>"; exit 1; }

# Clean state
rm -f .inject-state .inject-counter results/requests.log

# Setup injection if specified
if [[ -n "$MATCH" ]]; then
  echo "429 1 0 $MATCH" > .inject-state
fi

# Source environment
source env.sh
export PATH="/opt/homebrew/bin:$BENCH_DIR:$PATH"
export BCQ_BENCH_LOGFILE="$BENCH_DIR/results/requests.log"

# Get token for raw condition
TOKEN=$(jq -r '."http://3.basecamp.localhost:3001".access_token' ~/.config/basecamp/credentials.json 2>/dev/null || echo "")
BASE="http://3.basecampapi.localhost:3001/$BCQ_ACCOUNT_ID"

echo "=== Task $TASK ($CONDITION) ==="
echo "Project 1: $BCQ_BENCH_PROJECT_ID"
echo "Project 2: $BCQ_BENCH_PROJECT_ID_2"

# Task 12 execution
if [[ "$TASK" == "12" ]]; then
  TODAY=$(date +%Y-%m-%d)
  BASE_MARKER=$(yq -r '.fixtures.chain_marker' spec.yaml)
  # Generate run_id if not set (harness sets this, but allow standalone use)
  RUN_ID="${BCQ_BENCH_RUN_ID:-$(date +%Y%m%d-%H%M%S)-task12}"
  export BCQ_BENCH_RUN_ID="$RUN_ID"
  # Run-specific marker for validation
  MARKER="$BASE_MARKER $RUN_ID"
  echo "Run ID: $RUN_ID"
  echo "Marker: $MARKER"

  if [[ "$CONDITION" == "bcq-default" ]] || [[ "$CONDITION" == "bcq-nocache" ]]; then
    # BCQ execution
    for PID in "$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2"; do
      echo "--- Project $PID (bcq) ---"
      TODOS=$("$BENCH_DIR/../bin/bcq" todos --overdue --in "$PID" --json 2>&1)
      echo "$TODOS" | jq -r '.data[] | select(.title | startswith("Benchmark Overdue Todo")) | .id' | while read id; do
        [[ -z "$id" ]] && continue
        echo "Processing $id"
        "$BENCH_DIR/../bin/bcq" comment "$MARKER" --on "$id" --project "$PID" --json >/dev/null 2>&1
        "$BENCH_DIR/../bin/bcq" done "$id" --project "$PID" --json >/dev/null 2>&1
      done
    done
  else
    # Raw execution
    for PID in "$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2"; do
      echo "--- Project $PID (raw) ---"
      PROJECT_JSON=$(curl -sS -H "Authorization: Bearer $TOKEN" "$BASE/projects/$PID.json")
      TODOSET_ID=$(echo "$PROJECT_JSON" | jq -r '.dock[] | select(.name == "todoset") | .id')

      LISTS=$(curl -sS -H "Authorization: Bearer $TOKEN" "$BASE/buckets/$PID/todosets/$TODOSET_ID/todolists.json?per_page=50")

      echo "$LISTS" | jq -r '.[].id' | while read LIST_ID; do
        PAGE=1
        while true; do
          RESP=$(curl -sS -w "\n%{http_code}" -H "Authorization: Bearer $TOKEN" \
            "$BASE/buckets/$PID/todolists/$LIST_ID/todos.json?per_page=50&page=$PAGE")
          HTTP_CODE=$(echo "$RESP" | tail -1)
          TODOS=$(echo "$RESP" | sed '$d')

          if [[ "$HTTP_CODE" == "429" ]]; then
            echo "429 on page $PAGE, retrying..."
            sleep 2
            continue
          fi

          COUNT=$(echo "$TODOS" | jq 'length')
          [[ "$COUNT" -eq 0 ]] && break

          echo "$TODOS" | jq -r --arg today "$TODAY" '.[] | select(
            (.title | startswith("Benchmark Overdue Todo")) and
            (.due_on != null) and (.due_on < $today) and (.completed == false)
          ) | .id' | while read TODO_ID; do
            [[ -z "$TODO_ID" ]] && continue
            echo "Processing $TODO_ID"
            curl -sS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
              -d "{\"content\":\"$MARKER\"}" "$BASE/buckets/$PID/recordings/$TODO_ID/comments.json" >/dev/null
            curl -sS -X POST -H "Authorization: Bearer $TOKEN" "$BASE/buckets/$PID/todos/$TODO_ID/completion.json" >/dev/null
          done

          PAGE=$((PAGE + 1))
          [[ "$PAGE" -gt 10 ]] && break
        done
      done
    done
  fi
fi

echo ""
echo "=== Results ==="
TOTAL=$(wc -l < results/requests.log 2>/dev/null | tr -d ' ' || echo 0)
echo "Total requests: $TOTAL"
jq -r '.method' results/requests.log 2>/dev/null | sort | uniq -c || true

echo ""
echo "=== Validation ==="
./validate.sh check_overdue_chain
