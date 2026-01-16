#!/usr/bin/env bash
# Reset benchmark state between runs
# Cleans up items created by tasks, restores seeded state

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$BENCH_DIR/env.sh"

log() { echo "[reset] $*" >&2; }
warn() { echo "[reset] WARN: $*" >&2; }

# Uncomplete Todo Alpha (task 02)
reset_todo_alpha() {
  if [[ -n "${BCQ_BENCH_TODO_ALPHA_ID:-}" ]]; then
    log "Reopening Todo Alpha..."
    bcq reopen "$BCQ_BENCH_TODO_ALPHA_ID" --project "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
  fi
}

# Delete all test-created todos (not part of seed fixtures)
delete_test_artifacts() {
  log "Deleting test artifact todos..."

  # Get all todos and filter to non-seeded items
  # Expected seeded patterns:
  # - "Benchmark Seed Todo N"
  # - "Benchmark Todo Alpha"
  # - "Benchmark Todo Beta"
  # - "Search marker bcqbench2025 for validation"
  # - "Benchmark Overdue Todo N"
  local todos
  todos=$(bcq todos --in "$BCQ_BENCH_PROJECT_ID" --list "$BCQ_BENCH_TODOLIST_ID" --json 2>/dev/null | \
    jq -r '.data[] | select(
      (.content | startswith("Benchmark Seed Todo")) or
      (.content == "Benchmark Todo Alpha") or
      (.content == "Benchmark Todo Beta") or
      (.content | startswith("Search marker")) or
      (.content | startswith("Benchmark Overdue Todo"))
      | not
    ) | "\(.id)\t\(.content)"') || true

  if [[ -z "$todos" ]]; then
    log "  No test artifacts to clean"
    return
  fi

  echo "$todos" | while IFS=$'\t' read -r id content; do
    log "  Trashing: $id ($content)"
    bcq recordings trash "$id" --in "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
  done
}

# Delete benchmark comments (task 04)
delete_benchmark_comments() {
  if [[ -n "${BCQ_BENCH_MESSAGE_ID:-}" ]]; then
    log "Deleting benchmark comments on message..."
    local comments
    comments=$(bcq comments --on "$BCQ_BENCH_MESSAGE_ID" --in "$BCQ_BENCH_PROJECT_ID" --json 2>/dev/null | \
      jq -r '.data[] | select(.content | contains("Benchmark comment")) | .id') || true

    for id in $comments; do
      log "  Trashing comment $id..."
      bcq recordings trash "$id" --in "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
    done
  fi
}

# Restore Todo Beta position (task 05)
reset_todo_beta_position() {
  # Can't easily restore position, but the task just requires it NOT be first
  # Seed.sh creates it last, so just leave it
  log "Todo Beta position - no action needed (seed creates it last)"
}

# Delete "Benchmark List" todolist (task 06)
delete_benchmark_list() {
  log "Archiving 'Benchmark List' todolists..."
  local lists
  lists=$(bcq todolists --in "$BCQ_BENCH_PROJECT_ID" --json 2>/dev/null | \
    jq -r '.data[] | select(.name == "Benchmark List") | .id') || true

  for id in $lists; do
    log "  Archiving todolist $id..."
    bcq recordings archive "$id" --in "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
  done
}

# Reopen overdue todos (task 09 and 12)
# Uses stored fixture IDs instead of list endpoints (completed todos don't
# appear in Basecamp's list endpoints)
reset_overdue_todos() {
  log "Reopening overdue todos..."

  # Project 1 overdue todos
  local overdue_ids_1="${BCQ_BENCH_OVERDUE_IDS:-}"
  if [[ -n "$overdue_ids_1" ]]; then
    IFS=',' read -ra ids <<< "$overdue_ids_1"
    for id in "${ids[@]}"; do
      [[ -z "$id" ]] && continue
      log "  Reopening todo $id (P1)..."
      bcq reopen "$id" --project "$BCQ_BENCH_PROJECT_ID" --json >/dev/null 2>&1 || true
    done
  fi

  # Project 2 overdue todos
  local overdue_ids_2="${BCQ_BENCH_OVERDUE_IDS_2:-}"
  if [[ -n "$overdue_ids_2" ]]; then
    IFS=',' read -ra ids <<< "$overdue_ids_2"
    for id in "${ids[@]}"; do
      [[ -z "$id" ]] && continue
      log "  Reopening todo $id (P2)..."
      bcq reopen "$id" --project "$BCQ_BENCH_PROJECT_ID_2" --json >/dev/null 2>&1 || true
    done
  fi
}

# Delete chain_marker comments on overdue todos (task 12)
delete_overdue_comments() {
  # Read chain_marker from canonical source (spec.yaml)
  local marker
  marker=$(yq -r '.fixtures.chain_marker' "$BENCH_DIR/spec.yaml")
  log "Deleting '$marker' comments on overdue todos..."

  # Helper to delete comments matching marker on a todo
  _delete_comments() {
    local project_id="$1"
    local todo_id="$2"
    local marker="$3"

    local comments
    comments=$(bcq comments --on "$todo_id" --in "$project_id" --json 2>/dev/null | \
      jq -r --arg m "$marker" '.data[] | select(.content | contains($m)) | .id' 2>/dev/null) || true

    for cid in $comments; do
      log "    Trashing comment $cid..."
      bcq recordings trash "$cid" --in "$project_id" --json >/dev/null 2>&1 || true
    done
  }

  # Project 1
  local overdue_ids_1="${BCQ_BENCH_OVERDUE_IDS:-}"
  if [[ -n "$overdue_ids_1" ]]; then
    IFS=',' read -ra ids <<< "$overdue_ids_1"
    for id in "${ids[@]}"; do
      [[ -z "$id" ]] && continue
      log "  Checking todo $id (P1)..."
      _delete_comments "$BCQ_BENCH_PROJECT_ID" "$id" "$marker"
    done
  fi

  # Project 2
  local overdue_ids_2="${BCQ_BENCH_OVERDUE_IDS_2:-}"
  if [[ -n "$overdue_ids_2" ]]; then
    IFS=',' read -ra ids <<< "$overdue_ids_2"
    for id in "${ids[@]}"; do
      [[ -z "$id" ]] && continue
      log "  Checking todo $id (P2)..."
      _delete_comments "$BCQ_BENCH_PROJECT_ID_2" "$id" "$marker"
    done
  fi
}

# Clear cache
clear_cache() {
  log "Clearing cache..."
  rm -rf "$BCQ_CACHE_DIR"
  mkdir -p "$BCQ_CACHE_DIR"
}

# Clear request logs
clear_logs() {
  log "Clearing request logs..."
  rm -f "$BENCH_DIR/results"/*.log 2>/dev/null || true
  rm -f "$BCQ_BENCH_LOGFILE" 2>/dev/null || true
}

# Clear injection and loop detection state
clear_injection_state() {
  log "Clearing injection and loop detection state..."
  rm -f "$BENCH_DIR/.inject-state" 2>/dev/null || true
  rm -f "$BENCH_DIR/.inject-counter" 2>/dev/null || true
  rm -f "$BENCH_DIR/.loop-detect" 2>/dev/null || true
}

# Main
main() {
  local full=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --full) full=true; shift ;;
      *) warn "Unknown option: $1"; shift ;;
    esac
  done

  log "Resetting benchmark state..."

  # Always reset task-created items
  reset_todo_alpha
  delete_test_artifacts
  delete_benchmark_comments
  reset_todo_beta_position
  delete_benchmark_list
  delete_overdue_comments  # Must come before reset_overdue_todos
  reset_overdue_todos
  clear_cache
  clear_injection_state

  if [[ "$full" == "true" ]]; then
    clear_logs
    log "Full reset complete (including logs)"
  else
    log "Reset complete (use --full to also clear logs)"
  fi
}

main "$@"
