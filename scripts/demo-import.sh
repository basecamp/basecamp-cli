#!/usr/bin/env bash
# Run the deterministic Basecamp import demo flow.
set -euo pipefail

csv=""
mapping=""
destination=""
out="/tmp/basecamp-import-demo"
execute=0
repair_artifact=""
followup_out=""
reviewed=0

usage() {
  cat <<'USAGE'
Usage: scripts/demo-import.sh --csv tasks.csv --mapping mapping.json --destination destination.json [--out dir] [--execute]
       scripts/demo-import.sh --repair-artifact basecamp-import [--followup-out dir] [--reviewed]

Dry-run flow:
  1. basecamp import inspect
  2. basecamp import compile
  3. basecamp import plan --artifact
  4. basecamp import status --artifact

Execute flow with --execute:
  5. basecamp import preflight --artifact
  6. basecamp import execute --approved after confirmation
  7. basecamp import status --artifact

Recovery review flow with --repair-artifact:
  1. basecamp import status --artifact
  2. basecamp import repair --artifact
  3. optionally basecamp import followup --reviewed
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --csv)
      csv="${2:-}"; shift 2 ;;
    --mapping)
      mapping="${2:-}"; shift 2 ;;
    --destination)
      destination="${2:-}"; shift 2 ;;
    --out)
      out="${2:-}"; shift 2 ;;
    --execute)
      execute=1; shift ;;
    --repair-artifact)
      repair_artifact="${2:-}"; shift 2 ;;
    --followup-out)
      followup_out="${2:-}"; shift 2 ;;
    --reviewed)
      reviewed=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2 ;;
  esac
done

if [[ -n "$repair_artifact" ]]; then
  if [[ -n "$csv" || -n "$mapping" || -n "$destination" || "$execute" -eq 1 ]]; then
    echo "--repair-artifact cannot be combined with --csv, --mapping, --destination, or --execute" >&2
    exit 2
  fi
  if [[ ! -d "$repair_artifact" ]]; then
    echo "Repair artifact directory not found: $repair_artifact" >&2
    exit 1
  fi

  echo "== Artifact status =="
  basecamp import status --artifact "$repair_artifact" --json |
    jq '{ok, status: .data.status, counts: .data.counts, execution: .data.execution}'

  echo ""
  echo "== Repair review =="
  basecamp import repair --artifact "$repair_artifact" --json |
    jq '{ok, status: .data.status, created: .data.created, completed_operations: (.data.completed_operations | length), failed_operations: .data.failed_operations, pending_todos: .data.pending_todos, guidance: .data.guidance}'

  if [[ -z "$followup_out" ]]; then
    echo ""
    echo "Repair review complete. To create a follow-up artifact after review:"
    echo "  scripts/demo-import.sh --repair-artifact $repair_artifact --followup-out /tmp/basecamp-import-followup --reviewed"
    exit 0
  fi
  if [[ "$reviewed" -ne 1 ]]; then
    echo ""
    echo "Follow-up artifact creation requires --reviewed after checking Basecamp state and the repair summary." >&2
    exit 2
  fi

  rm -rf "$followup_out"
  echo ""
  echo "== Creating follow-up artifact =="
  basecamp import followup --artifact "$repair_artifact" --out "$followup_out" --reviewed --json |
    jq '{ok, status: .data.status, artifact_path: .data.artifact_path, counts: .data.manifest.counts, pending_todos: .data.pending_todos, guidance: .data.guidance}'

  echo ""
  echo "== Planning follow-up artifact =="
  basecamp import plan --artifact "$followup_out" --json | jq -r '.data.dry_run_markdown'
  echo "Follow-up artifact written to: $followup_out"
  exit 0
fi

if [[ -z "$csv" || -z "$mapping" || -z "$destination" ]]; then
  usage >&2
  exit 2
fi

if [[ ! -f "$csv" ]]; then
  echo "CSV not found: $csv" >&2
  exit 1
fi
if [[ ! -f "$mapping" ]]; then
  echo "Mapping JSON not found: $mapping" >&2
  exit 1
fi
if [[ ! -f "$destination" ]]; then
  echo "Destination JSON not found: $destination" >&2
  exit 1
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
inspection="$workdir/inspection.json"
plan="$workdir/plan.json"

rm -rf "$out"

echo "== Inspecting CSV =="
basecamp import inspect "$csv" --json > "$inspection"
jq -r '"Rows: \(.data.row_count), Columns: \(.data.columns | length), Status: \(.data.status)"' "$inspection"

echo ""
echo "== Compiling validated artifact =="
basecamp import compile \
  --inspection "$inspection" \
  --mapping "$mapping" \
  --destination "$destination" \
  --out "$out" \
  --json | jq '{ok, status: .data.status, artifact_path: .data.artifact_path, counts: .data.manifest.counts}'

echo ""
echo "== Planning from artifact =="
basecamp import plan --artifact "$out" --json > "$plan"
jq -r '.data.dry_run_markdown' "$plan"

echo ""
echo "== Local artifact status =="
basecamp import status --artifact "$out" --json |
  jq '{ok, status: .data.status, counts: .data.counts, guidance: .data.guidance}'

if [[ "$execute" -ne 1 ]]; then
  echo ""
  echo "Dry run complete. Artifact written to: $out"
  echo "Run again with --execute to preflight and execute after approval."
  exit 0
fi

echo ""
echo "== Preflight artifact =="
preflight_json="$workdir/preflight.json"
basecamp import preflight --artifact "$out" --json > "$preflight_json"
jq '{ok, status: .data.status, checks: .data.checks, collisions: .data.collisions, todo_collisions: .data.todo_collisions}' "$preflight_json"
preflight_status="$(jq -r '.data.status' "$preflight_json")"
if [[ "$preflight_status" != "passed" ]]; then
  echo "Preflight did not pass. Resolve blockers before execution." >&2
  exit 1
fi

read -r -p "Execute this import into Basecamp? Type 'approve' to continue: " approval
if [[ "$approval" != "approve" ]]; then
  echo "Execution canceled."
  exit 0
fi

echo ""
echo "== Executing approved import =="
set +e
execute_output="$(basecamp import execute --artifact "$out" --approved --json)"
execute_status=$?
set -e
echo "$execute_output" | jq '{ok, status: .data.status, created: .data.created, skipped: .data.skipped, error: .error}'

echo ""
echo "== Post-execution status =="
basecamp import status --artifact "$out" --json |
  jq '{ok, status: .data.status, created: .data.execution.created, operation_count: (.data.execution.operations | length), guidance: .data.guidance}'

if [[ "$execute_status" -ne 0 ]]; then
  echo ""
  echo "Execution failed. Review recovery state with:"
  echo "  scripts/demo-import.sh --repair-artifact $out"
fi
exit "$execute_status"
