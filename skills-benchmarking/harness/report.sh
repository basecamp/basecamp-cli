#!/usr/bin/env bash
# Generate summary report from harness metrics.json files
#
# Usage:
#   ./harness/report.sh                           # All tasks
#   ./harness/report.sh benchmarks/results 12     # Single task
#   ./harness/report.sh --analysis-template [path|-]

set -euo pipefail

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source pricing data for cost calculations
source "$HARNESS_DIR/lib/prices.sh"

RESULTS_DIR="benchmarks/results"
TASK_FILTER="" # optional: pass task id like "12"
ANALYSIS_TEMPLATE="false"
ANALYSIS_OUTPUT=""
RESULTS_DIR_SET="false"

usage() {
  cat <<'EOF'
Usage:
  ./harness/report.sh [results_dir] [task_id]
  ./harness/report.sh --analysis-template [path|-] [results_dir] [task_id]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --analysis-template)
      ANALYSIS_TEMPLATE="true"
      if [[ -n "${2:-}" ]] && [[ "${2:0:2}" != "--" ]]; then
        ANALYSIS_OUTPUT="$2"
        shift 2
      else
        shift
      fi
      ;;
    --analysis-template=*)
      ANALYSIS_TEMPLATE="true"
      ANALYSIS_OUTPUT="${1#*=}"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    -*)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      if [[ "$RESULTS_DIR_SET" != "true" ]]; then
        RESULTS_DIR="$1"
        RESULTS_DIR_SET="true"
        shift
      elif [[ -z "$TASK_FILTER" ]]; then
        TASK_FILTER="$1"
        shift
      else
        echo "Unknown argument: $1" >&2
        usage >&2
        exit 1
      fi
      ;;
  esac
done

files=("$RESULTS_DIR"/*/metrics.json)
if [[ ! -e "${files[0]}" ]]; then
  echo "No metrics.json files found in $RESULTS_DIR" >&2
  exit 1
fi

# Optional task filter
if [[ -n "$TASK_FILTER" ]]; then
  JQ_FILTER="select(.task == \"$TASK_FILTER\")"
else
  JQ_FILTER="."
fi

# Helper: print TSV, pretty if column exists
print_table() {
  if command -v column >/dev/null 2>&1; then
    column -t -s $'\t'
  else
    cat
  fi
}

echo "## Summary (grouped by task/strategy/model)"
jq -s "
  map($JQ_FILTER | . + {strategy: (.strategy // .condition)}) |
  sort_by(.task, .strategy, .model) |
  group_by(.task + \"|\" + .strategy + \"|\" + .model) |
  map({
    task: .[0].task,
    strategy: .[0].strategy,
    model: .[0].model,
    runs: length,
    pass_rate: (map(select(.success==true)) | length) / length,
    avg_http: (map(.metrics.http.requests) | add / length),
    avg_429: (map(.metrics.http.rate_limited) | add / length),
    avg_tokens: (map(.metrics.tokens.total // 0) | add / length),
    avg_prompt_bytes: (map(.metrics.prompt.total_bytes) | add / length),
    avg_duration_ms: (map(.metrics.duration_ms) | add / length),
    avg_turns: (map(.metrics.turns) | add / length)
  }) |
  ([
    \"task\",\"strategy\",\"model\",\"runs\",\"pass_rate\",
    \"avg_http\",\"avg_429\",\"avg_tokens\",\"avg_prompt_bytes\",\"avg_duration_ms\",\"avg_turns\"
  ] | @tsv),
  (map([
    .task, .strategy, .model, .runs,
    (.pass_rate*100 | tostring + \"%\"),
    (.avg_http|tostring), (.avg_429|tostring),
    (.avg_tokens|tostring), (.avg_prompt_bytes|tostring),
    (.avg_duration_ms|tostring), (.avg_turns|tostring)
  ] | @tsv) | .[])
" "${files[@]}" | print_table

echo ""
echo "## Per-run details"
jq -s "
  map($JQ_FILTER | . + {strategy: (.strategy // .condition)}) |
  sort_by(.task, .strategy, .model, .run_id) |
  ([
    \"run_id\",\"task\",\"strategy\",\"model\",\"success\",
    \"http_requests\",\"http_429s\",\"tokens_total\",\"prompt_bytes\",\"duration_ms\",\"turns\"
  ] | @tsv),
  (map([
    .run_id, .task, .strategy, .model, (.success|tostring),
    (.metrics.http.requests|tostring),
    (.metrics.http.rate_limited|tostring),
    (.metrics.tokens.total // 0 | tostring),
    (.metrics.prompt.total_bytes|tostring),
    (.metrics.duration_ms|tostring),
    (.metrics.turns|tostring)
  ] | @tsv) | .[])
" "${files[@]}" | print_table

echo ""
echo "## Per-run methodology checks (from validation output)"
{
  printf "%s\n" \
    "run_id	task	strategy	model	injection_expected	pagination_ok	recovered_429	run_marker_ok	time_valid	validation_reason"

  for metrics_file in "${files[@]}"; do
    run_dir=$(dirname "$metrics_file")
    validation_file="$run_dir/validation.txt"

    run_id=$(jq -r '.run_id' "$metrics_file")
    task=$(jq -r '.task' "$metrics_file")
    strategy=$(jq -r '.strategy // .condition' "$metrics_file")
    model=$(jq -r '.model' "$metrics_file")

    injection_expected="n/a"
    pagination_ok="n/a"
    recovered_429="n/a"
    run_marker_ok="n/a"
    time_valid="n/a"
    validation_reason="n/a"

    if [[ -f "$validation_file" ]]; then
      if grep -q "injection was configured" "$validation_file"; then
        injection_expected="yes"
      elif grep -q "No injection configured" "$validation_file"; then
        injection_expected="no"
      fi

      if grep -q "OK: Pagination verified" "$validation_file"; then
        pagination_ok="yes"
      elif grep -q "FAIL: No pagination evidence" "$validation_file"; then
        pagination_ok="no"
      fi

      if grep -q "OK: 429 received and recovered" "$validation_file"; then
        recovered_429="yes"
      elif grep -q "FAIL: No successful requests after 429" "$validation_file"; then
        recovered_429="no"
      fi

      if grep -q "marker comment" "$validation_file"; then
        run_marker_ok="no"
      elif grep -q "PASS: Overdue sweep" "$validation_file"; then
        run_marker_ok="yes"
      fi

      if grep -q "updated_at" "$validation_file" || grep -q "created before run_start" "$validation_file"; then
        time_valid="no"
      elif grep -q "Run started:" "$validation_file"; then
        time_valid="yes"
      fi

      validation_reason=$(grep -m1 '^FAIL:' "$validation_file" || grep -m1 '^PASS:' "$validation_file" || echo "unknown")
    fi

    printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" \
      "$run_id" "$task" "$strategy" "$model" "$injection_expected" "$pagination_ok" "$recovered_429" "$run_marker_ok" "$time_valid" "$validation_reason"
  done
} | print_table

echo ""
echo "## Per-run model outcome (metrics + validation)"
{
  printf "%s\n" \
    "run_id	task	strategy	model	success	request_count	prompt_bytes	duration_ms	validation_reason"

  for metrics_file in "${files[@]}"; do
    run_dir=$(dirname "$metrics_file")
    validation_file="$run_dir/validation.txt"

    run_id=$(jq -r '.run_id' "$metrics_file")
    task=$(jq -r '.task' "$metrics_file")
    strategy=$(jq -r '.strategy // .condition' "$metrics_file")
    model=$(jq -r '.model' "$metrics_file")
    success=$(jq -r '.success' "$metrics_file")
    request_count=$(jq -r '.metrics.http.requests' "$metrics_file")
    prompt_bytes=$(jq -r '.metrics.prompt.total_bytes' "$metrics_file")
    duration_ms=$(jq -r '.metrics.duration_ms' "$metrics_file")

    validation_reason="n/a"
    if [[ -f "$validation_file" ]]; then
      validation_reason=$(grep -m1 '^FAIL:' "$validation_file" || grep -m1 '^PASS:' "$validation_file" || echo "unknown")
    fi

    printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" \
      "$run_id" "$task" "$strategy" "$model" "$success" "$request_count" "$prompt_bytes" "$duration_ms" "$validation_reason"
  done
} | print_table

echo ""
echo "## Cost Analysis (per run)"
{
  printf "%s\n" \
    "run_id	strategy	model	success	input_tokens	output_tokens	cache_read	cache_write	cost_usd"

  for metrics_file in "${files[@]}"; do
    # Apply task filter if set
    if [[ -n "$TASK_FILTER" ]]; then
      task=$(jq -r '.task' "$metrics_file")
      [[ "$task" != "$TASK_FILTER" ]] && continue
    fi

    run_id=$(jq -r '.run_id' "$metrics_file")
    strategy=$(jq -r '.strategy // .condition' "$metrics_file")
    model=$(jq -r '.model' "$metrics_file")
    success=$(jq -r '.success' "$metrics_file")
    input_tokens=$(jq -r '.metrics.tokens.input // 0' "$metrics_file")
    output_tokens=$(jq -r '.metrics.tokens.output // 0' "$metrics_file")
    cache_read=$(jq -r '.metrics.tokens.cache_read // 0' "$metrics_file")
    cache_write=$(jq -r '.metrics.tokens.cache_write // 0' "$metrics_file")

    # Calculate cost using prices.sh
    cost=$(calc_cost "$model" "$input_tokens" "$output_tokens" "$cache_read" "$cache_write")

    printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t$%s\n" \
      "$run_id" "$strategy" "$model" "$success" "$input_tokens" "$output_tokens" "$cache_read" "$cache_write" "$cost"
  done
} | print_table

echo ""
echo "## Cost per Success (by strategy/model)"
{
  printf "%s\n" \
    "strategy	model	runs	successes	total_cost	cost_per_run	cost_per_success"

  # Aggregate by strategy and model
  declare -A runs_by_key=()
  declare -A successes_by_key=()
  declare -A cost_by_key=()

  for metrics_file in "${files[@]}"; do
    # Apply task filter if set
    if [[ -n "$TASK_FILTER" ]]; then
      task=$(jq -r '.task' "$metrics_file")
      [[ "$task" != "$TASK_FILTER" ]] && continue
    fi

    strategy=$(jq -r '.strategy // .condition' "$metrics_file")
    model=$(jq -r '.model' "$metrics_file")
    success=$(jq -r '.success' "$metrics_file")
    input_tokens=$(jq -r '.metrics.tokens.input // 0' "$metrics_file")
    output_tokens=$(jq -r '.metrics.tokens.output // 0' "$metrics_file")
    cache_read=$(jq -r '.metrics.tokens.cache_read // 0' "$metrics_file")
    cache_write=$(jq -r '.metrics.tokens.cache_write // 0' "$metrics_file")

    cost=$(calc_cost "$model" "$input_tokens" "$output_tokens" "$cache_read" "$cache_write")
    key="${strategy}|${model}"

    runs_by_key[$key]=$((${runs_by_key[$key]:-0} + 1))
    [[ "$success" == "true" ]] && successes_by_key[$key]=$((${successes_by_key[$key]:-0} + 1))
    cost_by_key[$key]=$(awk "BEGIN {print ${cost_by_key[$key]:-0} + $cost}")
  done

  # Output aggregated results
  for key in "${!runs_by_key[@]}"; do
    IFS='|' read -r strategy model <<< "$key"
    runs=${runs_by_key[$key]}
    successes=${successes_by_key[$key]:-0}
    total_cost=${cost_by_key[$key]}

    cost_per_run=$(awk "BEGIN {printf \"%.6f\", $total_cost / $runs}")

    if ((successes > 0)); then
      cost_per_success=$(awk "BEGIN {printf \"%.6f\", $total_cost / $successes}")
    else
      cost_per_success="∞"
    fi

    printf "%s\t%s\t%s\t%s\t$%s\t$%s\t$%s\n" \
      "$strategy" "$model" "$runs" "$successes" \
      "$(printf "%.4f" "$total_cost")" "$cost_per_run" "$cost_per_success"
  done | sort
} | print_table

echo ""
echo "## Strategy Summary (cost efficiency)"
{
  printf "%s\n" \
    "strategy	total_runs	total_successes	pass_rate	total_cost	avg_cost_per_run	avg_cost_per_success"

  # Aggregate by strategy only
  declare -A strat_runs=()
  declare -A strat_successes=()
  declare -A strat_cost=()

  for metrics_file in "${files[@]}"; do
    # Apply task filter if set
    if [[ -n "$TASK_FILTER" ]]; then
      task=$(jq -r '.task' "$metrics_file")
      [[ "$task" != "$TASK_FILTER" ]] && continue
    fi

    strategy=$(jq -r '.strategy // .condition' "$metrics_file")
    model=$(jq -r '.model' "$metrics_file")
    success=$(jq -r '.success' "$metrics_file")
    input_tokens=$(jq -r '.metrics.tokens.input // 0' "$metrics_file")
    output_tokens=$(jq -r '.metrics.tokens.output // 0' "$metrics_file")
    cache_read=$(jq -r '.metrics.tokens.cache_read // 0' "$metrics_file")
    cache_write=$(jq -r '.metrics.tokens.cache_write // 0' "$metrics_file")

    cost=$(calc_cost "$model" "$input_tokens" "$output_tokens" "$cache_read" "$cache_write")

    strat_runs[$strategy]=$((${strat_runs[$strategy]:-0} + 1))
    [[ "$success" == "true" ]] && strat_successes[$strategy]=$((${strat_successes[$strategy]:-0} + 1))
    strat_cost[$strategy]=$(awk "BEGIN {print ${strat_cost[$strategy]:-0} + $cost}")
  done

  # Output strategy summaries
  for strategy in "${!strat_runs[@]}"; do
    runs=${strat_runs[$strategy]}
    successes=${strat_successes[$strategy]:-0}
    total_cost=${strat_cost[$strategy]}

    pass_rate=$(awk "BEGIN {printf \"%.0f\", ($successes / $runs) * 100}")
    cost_per_run=$(awk "BEGIN {printf \"%.6f\", $total_cost / $runs}")

    if ((successes > 0)); then
      cost_per_success=$(awk "BEGIN {printf \"%.6f\", $total_cost / $successes}")
    else
      cost_per_success="∞"
    fi

    printf "%s\t%s\t%s\t%s%%\t$%s\t$%s\t$%s\n" \
      "$strategy" "$runs" "$successes" "$pass_rate" \
      "$(printf "%.4f" "$total_cost")" "$cost_per_run" "$cost_per_success"
  done | sort
} | print_table

echo ""
echo "## Caveats"
cat <<'EOF'
- GPT-5.* models use Chat Completions API. If your organization enforces Responses API only,
  OpenAI runs will fail. Migrate openai.sh adapter if needed.
- OpenAI cache write tokens are not reported by Chat Completions API; cost estimates for
  OpenAI models are lower bounds (actual costs may be higher if cache writes occur).
- Anthropic cache tokens (read + write) are fully tracked and costed.
EOF

if [[ "$ANALYSIS_TEMPLATE" == "true" ]]; then
  if [[ -z "$ANALYSIS_OUTPUT" ]]; then
    ANALYSIS_OUTPUT="$RESULTS_DIR/ANALYSIS.md"
  fi

  if [[ "$ANALYSIS_OUTPUT" == "-" ]]; then
    echo ""
    echo "## Analysis Template"
    if [[ -n "$TASK_FILTER" ]]; then
      RESULTS_DIR="$RESULTS_DIR" "$HARNESS_DIR/analysis-template.sh" --from-metrics --task "$TASK_FILTER"
    else
      RESULTS_DIR="$RESULTS_DIR" "$HARNESS_DIR/analysis-template.sh" --from-metrics
    fi
  else
    if [[ -n "$TASK_FILTER" ]]; then
      RESULTS_DIR="$RESULTS_DIR" "$HARNESS_DIR/analysis-template.sh" --from-metrics --task "$TASK_FILTER" > "$ANALYSIS_OUTPUT"
    else
      RESULTS_DIR="$RESULTS_DIR" "$HARNESS_DIR/analysis-template.sh" --from-metrics > "$ANALYSIS_OUTPUT"
    fi
    echo "" >&2
    echo "Analysis template written to: $ANALYSIS_OUTPUT" >&2
  fi
fi
