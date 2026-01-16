#!/usr/bin/env bash
# Generate a 1-page analysis template using models/conditions from a report
#
# Usage:
#   ./harness/analysis-template.sh [report.md] [task_id]
#   ./harness/analysis-template.sh --from-metrics [--task <id>]
#
# If no report is provided, uses the most recent BENCHMARK-REPORT-*.md.
# Falls back to metrics.json discovery when report parsing fails, or when
# --from-metrics is provided.

set -euo pipefail

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${RESULTS_DIR:-$HARNESS_DIR/../results}"

REPORT=""
TASK_ID=""
FROM_METRICS="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-metrics)
      FROM_METRICS="true"
      shift
      ;;
    --report)
      REPORT="${2:-}"
      shift 2
      ;;
    --task)
      TASK_ID="${2:-}"
      shift 2
      ;;
    *)
      if [[ -z "$REPORT" ]]; then
        REPORT="$1"
        shift
      elif [[ -z "$TASK_ID" ]]; then
        TASK_ID="$1"
        shift
      else
        echo "Unknown argument: $1" >&2
        exit 1
      fi
      ;;
  esac
done

if [[ -n "$REPORT" ]] && [[ ! -f "$REPORT" ]]; then
  echo "Report not found: $REPORT" >&2
  exit 1
fi

if [[ -z "$REPORT" ]]; then
  REPORT=$(ls -t "$RESULTS_DIR"/BENCHMARK-REPORT-*.md 2>/dev/null | head -1 || true)
fi

declare -a MODELS=()
declare -a CONDITIONS=()

add_unique() {
  local value="$1"
  local -n arr="$2"
  for item in "${arr[@]}"; do
    [[ "$item" == "$value" ]] && return 0
  done
  arr+=("$value")
}

parse_report_matrix() {
  local report="$1"
  local in_table="false"
  while IFS= read -r line; do
    if [[ "$line" =~ ^\| ]] && [[ "$line" == *"Model"* ]] && [[ "$line" == *"Condition"* ]]; then
      in_table="true"
      continue
    fi
    if [[ "$in_table" == "true" ]]; then
      [[ "$line" =~ ^\|[[:space:]]*- ]] && continue
      [[ ! "$line" =~ ^\| ]] && break
      local model condition
      model=$(echo "$line" | awk -F'|' '{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); print $2}')
      condition=$(echo "$line" | awk -F'|' '{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $3); print $3}')
      [[ -n "$model" ]] && add_unique "$model" MODELS
      [[ -n "$condition" ]] && add_unique "$condition" CONDITIONS
    fi
  done < "$report"
}

parse_report_summary() {
  local report="$1"
  local in_table="false"
  while IFS= read -r line; do
    if [[ "$line" == "## Summary (grouped by task/condition/model)"* ]]; then
      in_table="true"
      continue
    fi
    if [[ "$in_table" == "true" ]]; then
      [[ -z "$line" ]] && break
      # Skip header/separator lines
      if [[ "$line" == task* ]] || [[ "$line" =~ ^-+$ ]]; then
        continue
      fi
      local task condition model
      task=$(echo "$line" | awk '{print $1}')
      condition=$(echo "$line" | awk '{print $2}')
      model=$(echo "$line" | awk '{print $3}')
      if [[ -n "$model" ]] && [[ "$model" != "model" ]]; then
        add_unique "$model" MODELS
      fi
      if [[ -n "$condition" ]] && [[ "$condition" != "condition" ]]; then
        add_unique "$condition" CONDITIONS
      fi
      if [[ -z "$TASK_ID" ]] && [[ -n "$task" ]] && [[ "$task" != "task" ]]; then
        TASK_ID="$task"
      fi
    fi
  done < "$report"
}

parse_report_task() {
  local report="$1"
  # Prefer explicit "Task:" line if present
  local task_line
  task_line=$(grep -E '^Task:' "$report" 2>/dev/null | head -1 || true)
  if [[ -n "$task_line" ]]; then
    TASK_ID=$(echo "$task_line" | sed -E 's/^Task:[[:space:]]*//')
    return
  fi
  # Try "Task 12" in bold line
  task_line=$(grep -E '\*\*Task' "$report" 2>/dev/null | head -1 || true)
  if [[ -n "$task_line" ]]; then
    TASK_ID=$(echo "$task_line" | sed -E 's/.*Task[[:space:]]+([0-9]+).*/\1/' || true)
  fi
}

parse_metrics() {
  if ! command -v jq >/dev/null 2>&1; then
    return
  fi
  local files=("$RESULTS_DIR"/*/metrics.json)
  [[ ! -e "${files[0]}" ]] && return

  local jq_filter='.'
  if [[ -n "$TASK_ID" ]]; then
    jq_filter="select(.task == \"$TASK_ID\")"
  fi

  while IFS=$'\t' read -r model condition; do
    [[ -n "$model" ]] && add_unique "$model" MODELS
    [[ -n "$condition" ]] && add_unique "$condition" CONDITIONS
  done < <(jq -r "$jq_filter | [.model, .condition] | @tsv" "${files[@]}" 2>/dev/null || true)

  if [[ -z "$TASK_ID" ]]; then
    TASK_ID=$(jq -r '.task' "${files[0]}" 2>/dev/null || true)
  fi
}

if [[ "$FROM_METRICS" != "true" ]] && [[ -n "$REPORT" ]]; then
  parse_report_task "$REPORT"
  parse_report_matrix "$REPORT"
  parse_report_summary "$REPORT"
fi

if [[ ${#MODELS[@]} -eq 0 ]] || [[ ${#CONDITIONS[@]} -eq 0 ]] || [[ "$FROM_METRICS" == "true" ]]; then
  parse_metrics
fi

if [[ -z "$TASK_ID" ]]; then
  TASK_ID="<task>"
fi

# Order conditions with bcq/raw first if present
declare -a ORDERED_CONDITIONS=()
for preferred in bcq raw; do
  for cond in "${CONDITIONS[@]}"; do
    if [[ "$cond" == "$preferred" ]]; then
      add_unique "$cond" ORDERED_CONDITIONS
    fi
  done
done
for cond in "${CONDITIONS[@]}"; do
  add_unique "$cond" ORDERED_CONDITIONS
done

if [[ ${#MODELS[@]} -eq 0 ]]; then
  MODELS=("<models>")
fi
if [[ ${#ORDERED_CONDITIONS[@]} -eq 0 ]]; then
  ORDERED_CONDITIONS=("<conditions>")
fi

# Estimate runs per cell from metrics if available
RUNS_PER_CELL="<n>"
if command -v jq >/dev/null 2>&1; then
  if ls "$RESULTS_DIR"/*/metrics.json >/dev/null 2>&1; then
    counts=$(jq -r "select(.task == \"$TASK_ID\") | [.model, .condition] | @tsv" \
      "$RESULTS_DIR"/*/metrics.json 2>/dev/null | sort | uniq -c || true)
    if [[ -n "$counts" ]]; then
      RUNS_PER_CELL=$(echo "$counts" | awk '{if($1>max) max=$1} END {print max}')
    fi
  fi
fi

models_csv=$(IFS=', '; echo "${MODELS[*]}")
conds_csv=$(IFS=', '; echo "${ORDERED_CONDITIONS[*]}")

cat <<EOF
# bcq vs Raw API — Benchmark Analysis (Task ${TASK_ID})

Date: $(date +%Y-%m-%d)  
Task: ${TASK_ID}  
Runs: ${RUNS_PER_CELL} per cell  
Conditions: ${conds_csv}  
Models: ${models_csv}  
Substitutions (if any): <note substitutions>

## Executive Summary (3–4 sentences)
- <primary result: pass rate gap>
- <cost per success comparison>
- <context/input token savings>
- <hypothesis confirmed / partial / not confirmed>

## Hypothesis & Pass Criteria
**Hypothesis:** bcq improves reliability and lowers cost for cheap models vs raw API.

**Pass criteria:**
- ≥20pp higher pass rate for bcq on cheap models
- ≥30% lower cost per success for bcq in ≥3/5 models
- ≥40% lower input tokens for bcq at equal success

**Outcome:** ✅ / ⚠️ / ❌

## Results Matrix
| Model | Condition | Pass Rate | Avg Input Tokens | Avg Cost/Run | Cost per Success |
|------|-----------|-----------|------------------|--------------|------------------|
EOF

for model in "${MODELS[@]}"; do
  for cond in "${ORDERED_CONDITIONS[@]}"; do
    printf "| %s | %s |  |  |  |  |\n" "$model" "$cond"
  done
done

cat <<'EOF'

## Reliability Findings
- Cheap models: <summary + failure modes>
- Strong models: <summary>
- Failure taxonomy: NO_WRITES / EARLY_EXIT / PAGINATION_MISS / etc.

## Cost & Context Findings
- Avg input tokens (bcq vs raw)
- Cost per success (bcq vs raw)
- Turn count correlation (if notable)

## Interpretation
- Where the invariant lives (tool vs model)
- Why raw fails (compliance burden, pagination, retries, chaining)
- When raw is “good enough” (strong models, low‑complexity tasks)

## Decision
- Ship bcq as default for agent workflows? (yes/no + rationale)
- Keep raw skill for expert use? (yes/no + scope)

## Caveats
- GPT‑5 Chat Completions compatibility (if relevant)
- OpenAI cache write tokens not tracked (costs are lower bounds)
- Single task benchmark (Task 12), not full API surface

## Appendix
- Report file: <path>
- Run IDs: <paste list>
EOF
