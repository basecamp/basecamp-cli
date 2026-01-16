#!/bin/bash
set -e
cd /Users/jeremy/Work/basecamp/bcq/benchmarks
source env.sh
for PROJECT in "$BCQ_BENCH_PROJECT_ID" "$BCQ_BENCH_PROJECT_ID_2"; do
  echo "Processing project $PROJECT"
  OUT=$(/opt/homebrew/bin/bash ../bin/bcq todos --overdue --in "$PROJECT" --json)
  IDS=$(echo "$OUT" | jq -r \.data[] | select(.title | startswith("Benchmark Overdue Todo")) | .id)
  COUNT=0
  PROCESSED_IDS=""
  for TID in $IDS; do
    COUNT=$((COUNT+1))
    PROCESSED_IDS="$PROCESSED_IDS $TID"
    /opt/homebrew/bin/bash ../bin/bcq comment "Processed BenchChain $BCQ_BENCH_RUN_ID" --on "$TID" --project "$PROJECT" --json
    /opt/homebrew/bin/bash ../bin/bcq done "$TID" --project "$PROJECT" --json
  done
  echo "Project $PROJECT: processed $COUNT todos: $PROCESSED_IDS"
done
