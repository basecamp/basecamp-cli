#!/usr/bin/env bash
set -euo pipefail
source env.sh

# Export environment values for this session
TODAY="${TODAY}"
BASE="${BCQ_API_BASE}"
AUTH_HEADER="Authorization: Bearer ${BCQ_ACCESS_TOKEN}"
CONTENT_HEADER="Content-Type: application/json"
PROJECTS=("${BCQ_BENCH_PROJECT_ID}" "${BCQ_BENCH_PROJECT_ID_2}")

retry_get() {
  local url="$1" tmp
  tmp=$(mktemp)
  while true; do
    http_code=$(curl -sS -H "$AUTH_HEADER" -H "$CONTENT_HEADER" -X GET "$url" -o "$tmp" -w "%{http_code}")
    if [ "$http_code" = "429" ]; then
      sleep 2
      continue
    else
      cat "$tmp"; rm -f "$tmp"; return 0
    fi
  done
}

retry_post() {
  local url="$1" data="$2" tmp
  tmp=$(mktemp)
  while true; do
    http_code=$(curl -sS -H "$AUTH_HEADER" -H "$CONTENT_HEADER" -X POST -d "$data" "$url" -o "$tmp" -w "%{http_code}")
    if [ "$http_code" = "429" ]; then
      sleep 2; continue
    else
      rm -f "$tmp"; return 0
    fi
  done
}

TOTAL=0
for PRJ in "${PROJECTS[@]}"; do
  echo "Starting sweep for project $PRJ" 1>&2
  PROJ_JSON=$(retry_get "$BASE/projects/$PRJ.json")
  TODOS_IDS=$(echo "$PROJ_JSON" | jq -r .dock[].id
