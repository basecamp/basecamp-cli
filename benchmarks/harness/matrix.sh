#!/usr/bin/env bash
# Run full benchmark matrix across models and conditions
#
# Requires bash 4.0+ for associative arrays

if ((BASH_VERSINFO[0] < 4)); then
  echo "Error: bash 4.0+ required (found ${BASH_VERSION})" >&2
  exit 1
fi
#
# Usage:
#   ./harness/matrix.sh --task 12
#   ./harness/matrix.sh --task 12 --models "claude-sonnet,gpt-4o"
#   ./harness/matrix.sh --task 12 --strategys "bcq"
#   ./harness/matrix.sh --task 12 --runs 5   # 5 runs per cell for statistics

set -euo pipefail

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(dirname "$HARNESS_DIR")"

# Source pricing data
source "$HARNESS_DIR/lib/prices.sh"

# Triage function for run validity
triage_run_result() {
  local run_dir="$1"
  "$HARNESS_DIR/triage.sh" --update "$run_dir" 2>/dev/null || echo "ERROR:triage_failed:true"
}

should_rerun_class() {
  local class="$1"
  # Classes that should trigger auto-rerun
  [[ " HARNESS_BUG INFRA_FLAKE DATA_ISSUE " == *" $class "* ]]
}

# Track prompt hashes per task/condition for cohort integrity
declare -A PROMPT_HASHES

check_cohort_integrity() {
  local result_file="$1"
  local task condition prompt_hash
  task=$(jq -r '.task' "$result_file")
  condition=$(jq -r '.condition' "$result_file")
  prompt_hash=$(jq -r '.prompt_hash // ""' "$result_file")

  if [[ -z "$prompt_hash" ]]; then
    return 0  # No hash to check
  fi

  local key="${task}:${condition}"
  if [[ -n "${PROMPT_HASHES[$key]:-}" ]]; then
    if [[ "${PROMPT_HASHES[$key]}" != "$prompt_hash" ]]; then
      echo "  ERROR: Cohort split! Prompt hash changed for $key"
      echo "    Previous: ${PROMPT_HASHES[$key]}"
      echo "    Current:  $prompt_hash"
      echo "    This run is INVALID and must be rerun with consistent prompts"

      # Mark run as invalid in metrics.json
      jq --arg status "invalid" \
         --arg class "HARNESS_BUG" \
         --arg detail "cohort_split_prompt_changed" \
         --argjson rerun true \
         '. + {run_status: $status, failure_class: $class, failure_detail: $detail, rerun_recommended: $rerun}' \
         "$result_file" > "${result_file}.tmp" && mv "${result_file}.tmp" "$result_file"

      return 1  # Signal cohort split
    fi
  else
    PROMPT_HASHES[$key]="$prompt_hash"
  fi
  return 0
}

# === Configuration ===
TASK=""
MODELS="claude-sonnet,claude-haiku,gpt-5-turbo,gpt-5-mini"
CONDITIONS="bcq,raw"
RUNS=1
DELAY=2
RERUN_INVALID=0  # Max retries for invalid runs (0=disabled)
PROMPT_REGIME="baseline"  # Cohort tag: baseline, optimized_contract, etc.

# === Argument Parsing ===
while [[ $# -gt 0 ]]; do
  case "$1" in
    --task|-t)
      TASK="$2"
      shift 2
      ;;
    --models|-m)
      MODELS="$2"
      shift 2
      ;;
    --strategys|-c)
      CONDITIONS="$2"
      shift 2
      ;;
    --runs|-n)
      RUNS="$2"
      shift 2
      ;;
    --delay)
      DELAY="$2"
      shift 2
      ;;
    --rerun-invalid)
      RERUN_INVALID="$2"
      shift 2
      ;;
    --prompt-regime)
      PROMPT_REGIME="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

[[ -z "$TASK" ]] && { echo "Usage: $0 --task <id> [--models m1,m2] [--strategys c1,c2] [--runs N]" >&2; exit 1; }

# === Matrix Execution ===
IFS=',' read -ra MODEL_LIST <<< "$MODELS"
IFS=',' read -ra CONDITION_LIST <<< "$CONDITIONS"

TOTAL=$((${#MODEL_LIST[@]} * ${#CONDITION_LIST[@]} * RUNS))
CURRENT=0
declare -a RESULTS=()

echo "=== Benchmark Matrix ==="
echo "Task: $TASK"
echo "Models: ${MODELS}"
echo "Conditions: ${CONDITIONS}"
echo "Runs per cell: $RUNS"
echo "Total runs: $TOTAL"
echo "Prompt regime: $PROMPT_REGIME"
if ((RERUN_INVALID > 0)); then
  echo "Rerun invalid: up to $RERUN_INVALID retries"
fi
echo ""

for model in "${MODEL_LIST[@]}"; do
  for condition in "${CONDITION_LIST[@]}"; do
    for ((run=1; run<=RUNS; run++)); do
      ((CURRENT++)) || true

      if ((RUNS > 1)); then
        echo "[$CURRENT/$TOTAL] Running: task=$TASK condition=$condition model=$model (run $run/$RUNS)"
      else
        echo "[$CURRENT/$TOTAL] Running: task=$TASK condition=$condition model=$model"
      fi

      # Reset fixtures
      echo "  Resetting fixtures..."
      "$BENCH_DIR/reset.sh" --full >/dev/null 2>&1 || true

      # Export prompt regime for this cohort
      export BCQ_BENCH_PROMPT_REGIME="$PROMPT_REGIME"

      # Run benchmark with potential reruns
      attempt=0
      max_attempts=$((1 + RERUN_INVALID))
      run_accepted=false

      while ((attempt < max_attempts)) && [[ "$run_accepted" != "true" ]]; do
        ((attempt++)) || true

        if ((attempt > 1)); then
          echo "  Retry $((attempt-1))/$RERUN_INVALID after invalid run..."
          # Reset fixtures before retry
          "$BENCH_DIR/reset.sh" --full >/dev/null 2>&1 || true
          sleep "$DELAY"
        fi

        # Run benchmark
        result_file=$("$HARNESS_DIR/run.sh" --task "$TASK" --strategy "$condition" --model "$model" 2>&1 | \
          tee /dev/stderr | \
          grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4 || echo "")

        if [[ -n "$result_file" ]] && [[ -f "$BENCH_DIR/results/$result_file/metrics.json" ]]; then
          run_dir="$BENCH_DIR/results/$result_file"

          # Triage the run
          triage_result=$(triage_run_result "$run_dir")
          failure_class="${triage_result%%:*}"

          echo "  Triage: $triage_result"

          # Check cohort integrity - if split, mark invalid and retry
          cohort_ok=true
          if ! check_cohort_integrity "$run_dir/metrics.json"; then
            cohort_ok=false
            failure_class="HARNESS_BUG"  # Override for rerun logic
          fi

          # Check if should rerun (either triage failure or cohort split)
          if ((RERUN_INVALID > 0)) && { should_rerun_class "$failure_class" || [[ "$cohort_ok" == "false" ]]; } && ((attempt < max_attempts)); then
            echo "  Run invalid ($failure_class), will retry..."
          else
            run_accepted=true
            RESULTS+=("$run_dir/metrics.json")
          fi
        else
          # No result file created - severe failure
          echo "  ERROR: No result file created"
          run_accepted=true  # Don't retry, something is very wrong
        fi
      done

      # Delay between runs
      if (( CURRENT < TOTAL )); then
        sleep "$DELAY"
      fi
    done
  done
done

echo ""
echo "=== Matrix Complete ==="
echo ""

# === Aggregate Results ===
if [[ ${#RESULTS[@]} -gt 0 ]]; then
  echo "=== Results by Run ==="

  # Create summary table header
  printf "%-15s %-8s %-7s %-8s %-12s %-12s %-10s %-10s %-10s\n" \
    "MODEL" "COND" "PASS" "STATUS" "TOKENS" "COST" "HTTP" "DURATION" "FAILURE"
  printf "%-15s %-8s %-7s %-8s %-12s %-12s %-10s %-10s %-10s\n" \
    "---------------" "--------" "-------" "--------" "------------" "------------" "----------" "----------" "----------"

  for result in "${RESULTS[@]}"; do
    if [[ -f "$result" ]]; then
      # Read metrics
      model=$(jq -r '.model' "$result")
      condition=$(jq -r '.condition' "$result")
      success=$(jq -r '.success' "$result")
      run_status=$(jq -r '.run_status // "-"' "$result")
      failure_class=$(jq -r '.failure_class // "-"' "$result")
      input_tokens=$(jq -r '.metrics.tokens.input // 0' "$result")
      output_tokens=$(jq -r '.metrics.tokens.output // 0' "$result")
      cache_read=$(jq -r '.metrics.tokens.cache_read // 0' "$result")
      cache_write=$(jq -r '.metrics.tokens.cache_write // 0' "$result")
      total_tokens=$(jq -r '.metrics.tokens.total // 0' "$result")
      http_reqs=$(jq -r '.metrics.http.requests // 0' "$result")
      duration=$(jq -r '.metrics.duration_ms // 0' "$result")

      # Calculate cost
      cost=$(calc_cost "$model" "$input_tokens" "$output_tokens" "$cache_read" "$cache_write")

      printf "%-15s %-8s %-7s %-8s %-12s $%-11s %-10s %-10s %-10s\n" \
        "${model:0:15}" "$condition" "$success" "$run_status" "$total_tokens" "$cost" "$http_reqs" "${duration}ms" "${failure_class:0:10}"
    fi
  done

  echo ""

  # === Per-Cell Aggregates ===
  if ((RUNS > 1)); then
    echo "=== Aggregated by Model/Condition (n=$RUNS) ==="
    printf "%-15s %-8s %-12s %-12s %-12s %-12s\n" \
      "MODEL" "COND" "PASS_RATE" "AVG_TOKENS" "AVG_COST" "COST/SUCCESS"
    printf "%-15s %-8s %-12s %-12s %-12s %-12s\n" \
      "---------------" "--------" "------------" "------------" "------------" "------------"

    for model in "${MODEL_LIST[@]}"; do
      for condition in "${CONDITION_LIST[@]}"; do
        # Filter results for this cell
        cell_results=()
        for result in "${RESULTS[@]}"; do
          if [[ -f "$result" ]]; then
            r_model=$(jq -r '.model' "$result")
            r_cond=$(jq -r '.condition' "$result")
            if [[ "$r_model" == "$model" ]] && [[ "$r_cond" == "$condition" ]]; then
              cell_results+=("$result")
            fi
          fi
        done

        if [[ ${#cell_results[@]} -gt 0 ]]; then
          # Calculate aggregates
          total_success=0
          total_tokens=0
          total_cost=0

          for result in "${cell_results[@]}"; do
            success=$(jq -r '.success' "$result")
            [[ "$success" == "true" ]] && ((total_success++)) || true
            tokens=$(jq -r '.metrics.tokens.total // 0' "$result")
            input_t=$(jq -r '.metrics.tokens.input // 0' "$result")
            output_t=$(jq -r '.metrics.tokens.output // 0' "$result")
            cache_r=$(jq -r '.metrics.tokens.cache_read // 0' "$result")
            cache_w=$(jq -r '.metrics.tokens.cache_write // 0' "$result")
            cost=$(calc_cost "$model" "$input_t" "$output_t" "$cache_r" "$cache_w")

            total_tokens=$((total_tokens + tokens))
            total_cost=$(awk "BEGIN {print $total_cost + $cost}")
          done

          n=${#cell_results[@]}
          pass_rate=$(awk "BEGIN {printf \"%.0f%%\", ($total_success / $n) * 100}")
          avg_tokens=$((total_tokens / n))
          avg_cost=$(awk "BEGIN {printf \"%.4f\", $total_cost / $n}")

          if ((total_success > 0)); then
            cost_per_success=$(awk "BEGIN {printf \"%.4f\", $total_cost / $total_success}")
          else
            cost_per_success="∞"
          fi

          printf "%-15s %-8s %-12s %-12s $%-11s $%-11s\n" \
            "${model:0:15}" "$condition" "$pass_rate" "$avg_tokens" "$avg_cost" "$cost_per_success"
        fi
      done
    done

    echo ""
  fi

  # === Summary Stats ===
  echo "=== Summary ==="

  # Calculate totals by condition
  for condition in "${CONDITION_LIST[@]}"; do
    total_runs=0
    total_success=0
    total_cost=0

    for result in "${RESULTS[@]}"; do
      if [[ -f "$result" ]]; then
        r_cond=$(jq -r '.condition' "$result")
        if [[ "$r_cond" == "$condition" ]]; then
          ((total_runs++)) || true
          success=$(jq -r '.success' "$result")
          [[ "$success" == "true" ]] && ((total_success++)) || true

          model=$(jq -r '.model' "$result")
          input_t=$(jq -r '.metrics.tokens.input // 0' "$result")
          output_t=$(jq -r '.metrics.tokens.output // 0' "$result")
          cache_r=$(jq -r '.metrics.tokens.cache_read // 0' "$result")
          cache_w=$(jq -r '.metrics.tokens.cache_write // 0' "$result")
          cost=$(calc_cost "$model" "$input_t" "$output_t" "$cache_r" "$cache_w")
          total_cost=$(awk "BEGIN {print $total_cost + $cost}")
        fi
      fi
    done

    if ((total_runs > 0)); then
      pass_rate=$(awk "BEGIN {printf \"%.0f\", ($total_success / $total_runs) * 100}")
      echo "$condition: $total_success/$total_runs ($pass_rate%) - Total cost: \$$(printf "%.4f" "$total_cost")"
    fi
  done

  echo ""

  # Export full JSON
  echo "Full results exported to: $BENCH_DIR/results/matrix-$(date +%Y%m%d-%H%M%S).json"
  jq -s '.' "${RESULTS[@]}" > "$BENCH_DIR/results/matrix-$(date +%Y%m%d-%H%M%S).json" 2>/dev/null
fi
