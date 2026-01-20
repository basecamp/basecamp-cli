#!/usr/bin/env bash
# Run full benchmark matrix across models and strategies
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
#   ./harness/matrix.sh --task 12 --strategies "bcq-full"
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

# Track prompt hashes per task/strategy for cohort integrity
declare -A PROMPT_HASHES

check_cohort_integrity() {
  local result_file="$1"
  local task strategy prompt_hash
  task=$(jq -r '.task' "$result_file")
  strategy=$(jq -r '.strategy // .condition' "$result_file")
  prompt_hash=$(jq -r '.prompt_hash // ""' "$result_file")

  if [[ -z "$prompt_hash" ]]; then
    return 0  # No hash to check
  fi

  local key="${task}:${strategy}"
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
# Default strategies from strategies.json (canonical source)
STRATEGIES_FILE="$BENCH_DIR/strategies.json"
if [[ -f "$STRATEGIES_FILE" ]]; then
  STRATEGIES=$(jq -r '.strategies | keys[]' "$STRATEGIES_FILE" 2>/dev/null | sort | tr '\n' ',' | sed 's/,$//')
else
  STRATEGIES="bcq-full,api-docs-with-curl-examples"  # fallback
fi
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
    --strategies|-s)
      STRATEGIES="$2"
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

[[ -z "$TASK" ]] && { echo "Usage: $0 --task <id> [--models m1,m2] [--strategies s1,s2] [--runs N]" >&2; exit 1; }

# === Strategy Validation ===
# Fail fast if any requested strategy is not in strategies.json
validate_strategies() {
  if [[ ! -f "$STRATEGIES_FILE" ]]; then
    echo "Warning: $STRATEGIES_FILE not found, skipping strategy validation" >&2
    return 0
  fi

  local valid_strategies invalid=()
  valid_strategies=$(jq -r '.strategies | keys[]' "$STRATEGIES_FILE" 2>/dev/null | tr '\n' ' ')

  for strategy in "${STRATEGY_LIST[@]}"; do
    if ! echo " $valid_strategies " | grep -q " $strategy "; then
      invalid+=("$strategy")
    fi
  done

  if [[ ${#invalid[@]} -gt 0 ]]; then
    echo "ERROR: Unknown strategies: ${invalid[*]}" >&2
    echo "Valid strategies: $valid_strategies" >&2
    echo "" >&2
    echo "Add missing strategies to $STRATEGIES_FILE or fix --strategies argument" >&2
    return 1
  fi
  return 0
}

# === Matrix Execution ===
IFS=',' read -ra MODEL_LIST <<< "$MODELS"
IFS=',' read -ra STRATEGY_LIST <<< "$STRATEGIES"

# Validate strategies before running
if ! validate_strategies; then
  exit 1
fi

TOTAL=$((${#MODEL_LIST[@]} * ${#STRATEGY_LIST[@]} * RUNS))
CURRENT=0
declare -a RESULTS=()

echo "=== Benchmark Matrix ==="
echo "Task: $TASK"
echo "Models: ${MODELS}"
echo "Strategies: ${STRATEGIES}"
echo "Runs per cell: $RUNS"
echo "Total runs: $TOTAL"
echo "Prompt regime: $PROMPT_REGIME"
if ((RERUN_INVALID > 0)); then
  echo "Rerun invalid: up to $RERUN_INVALID retries"
fi
echo ""

for model in "${MODEL_LIST[@]}"; do
  for strategy in "${STRATEGY_LIST[@]}"; do
    for ((run=1; run<=RUNS; run++)); do
      ((CURRENT++)) || true

      if ((RUNS > 1)); then
        echo "[$CURRENT/$TOTAL] Running: task=$TASK strategy=$strategy model=$model (run $run/$RUNS)"
      else
        echo "[$CURRENT/$TOTAL] Running: task=$TASK strategy=$strategy model=$model"
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
        # Note: grep pattern handles both compact and pretty-printed JSON
        result_file=$("$HARNESS_DIR/run.sh" --task "$TASK" --strategy "$strategy" --model "$model" 2>&1 | \
          tee /dev/stderr | \
          grep -oE '"run_id":\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/' || echo "")

        if [[ -n "$result_file" ]] && [[ -f "$BENCH_DIR/results/$result_file/metrics.json" ]]; then
          run_dir="$BENCH_DIR/results/$result_file"

          # Triage the run
          triage_result=$(triage_run_result "$run_dir")
          failure_class="${triage_result%%:*}"

          # Print triage result - highlight invalid runs
          if [[ "$failure_class" != "VALID" ]] && [[ "$failure_class" != "MODEL_FAILURE" ]]; then
            echo "  ⚠️  INVALID: $triage_result"
          else
            echo "  Triage: $triage_result"
          fi

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
  printf "%-15s %-10s %-7s %-8s %-12s %-12s %-10s %-10s %-10s\n" \
    "MODEL" "STRATEGY" "PASS" "STATUS" "TOKENS" "COST" "HTTP" "DURATION" "FAILURE"
  printf "%-15s %-10s %-7s %-8s %-12s %-12s %-10s %-10s %-10s\n" \
    "---------------" "----------" "-------" "--------" "------------" "------------" "----------" "----------" "----------"

  for result in "${RESULTS[@]}"; do
    if [[ -f "$result" ]]; then
      # Read metrics
      model=$(jq -r '.model' "$result")
      strategy=$(jq -r '.strategy // .condition' "$result")
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

      printf "%-15s %-10s %-7s %-8s %-12s $%-11s %-10s %-10s %-10s\n" \
        "${model:0:15}" "$strategy" "$success" "$run_status" "$total_tokens" "$cost" "$http_reqs" "${duration}ms" "${failure_class:0:10}"
    fi
  done

  echo ""

  # === Per-Cell Aggregates ===
  if ((RUNS > 1)); then
    echo "=== Aggregated by Model/Strategy (n=$RUNS) ==="
    printf "%-15s %-10s %-12s %-12s %-12s %-12s\n" \
      "MODEL" "STRATEGY" "PASS_RATE" "AVG_TOKENS" "AVG_COST" "COST/SUCCESS"
    printf "%-15s %-10s %-12s %-12s %-12s %-12s\n" \
      "---------------" "----------" "------------" "------------" "------------" "------------"

    for model in "${MODEL_LIST[@]}"; do
      for strategy in "${STRATEGY_LIST[@]}"; do
        # Filter results for this cell
        cell_results=()
        for result in "${RESULTS[@]}"; do
          if [[ -f "$result" ]]; then
            r_model=$(jq -r '.model' "$result")
            r_strat=$(jq -r '.strategy // .condition' "$result")
            if [[ "$r_model" == "$model" ]] && [[ "$r_strat" == "$strategy" ]]; then
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

          printf "%-15s %-10s %-12s %-12s $%-11s $%-11s\n" \
            "${model:0:15}" "$strategy" "$pass_rate" "$avg_tokens" "$avg_cost" "$cost_per_success"
        fi
      done
    done

    echo ""
  fi

  # === Summary Stats ===
  echo "=== Summary ==="

  # Calculate totals by strategy
  for strategy in "${STRATEGY_LIST[@]}"; do
    total_runs=0
    total_success=0
    total_cost=0

    for result in "${RESULTS[@]}"; do
      if [[ -f "$result" ]]; then
        r_strat=$(jq -r '.strategy // .condition' "$result")
        if [[ "$r_strat" == "$strategy" ]]; then
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
      echo "$strategy: $total_success/$total_runs ($pass_rate%) - Total cost: \$$(printf "%.4f" "$total_cost")"
    fi
  done

  echo ""

  # === Efficiency Comparison ===
  echo "=== Efficiency Comparison (Valid Runs Only) ==="
  printf "%-12s %-8s %-8s %-10s %-12s %-12s %-10s\n" \
    "STRATEGY" "RUNS" "PASS%" "AVG_TURNS" "AVG_INPUT_T" "COST/SUCCESS" "RELATIVE"
  printf "%-12s %-8s %-8s %-10s %-12s %-12s %-10s\n" \
    "------------" "--------" "--------" "----------" "------------" "------------" "----------"

  # Collect per-strategy stats (valid runs only)
  declare -A STRAT_RUNS STRAT_SUCCESS STRAT_TURNS STRAT_INPUT STRAT_COST
  for strategy in "${STRATEGY_LIST[@]}"; do
    STRAT_RUNS[$strategy]=0
    STRAT_SUCCESS[$strategy]=0
    STRAT_TURNS[$strategy]=0
    STRAT_INPUT[$strategy]=0
    STRAT_COST[$strategy]=0
  done

  for result in "${RESULTS[@]}"; do
    if [[ -f "$result" ]]; then
      run_status=$(jq -r '.run_status // "valid"' "$result")
      [[ "$run_status" != "valid" ]] && continue  # Skip invalid runs

      r_strat=$(jq -r '.strategy // .condition' "$result")
      success=$(jq -r '.success' "$result")
      turns=$(jq -r '.metrics.turns // 0' "$result")
      input_t=$(jq -r '.metrics.tokens.input // 0' "$result")
      output_t=$(jq -r '.metrics.tokens.output // 0' "$result")
      cache_r=$(jq -r '.metrics.tokens.cache_read // 0' "$result")
      cache_w=$(jq -r '.metrics.tokens.cache_write // 0' "$result")
      model=$(jq -r '.model' "$result")
      cost=$(calc_cost "$model" "$input_t" "$output_t" "$cache_r" "$cache_w")

      ((STRAT_RUNS[$r_strat]++)) || true
      [[ "$success" == "true" ]] && ((STRAT_SUCCESS[$r_strat]++)) || true
      STRAT_TURNS[$r_strat]=$((STRAT_TURNS[$r_strat] + turns))
      STRAT_INPUT[$r_strat]=$((STRAT_INPUT[$r_strat] + input_t))
      STRAT_COST[$r_strat]=$(awk "BEGIN {print ${STRAT_COST[$r_strat]} + $cost}")
    fi
  done

  # Find baseline cost for relative comparison (first strategy with successes)
  baseline_cps=""
  for strategy in "${STRATEGY_LIST[@]}"; do
    if [[ ${STRAT_SUCCESS[$strategy]} -gt 0 ]]; then
      baseline_cps=$(awk "BEGIN {printf \"%.6f\", ${STRAT_COST[$strategy]} / ${STRAT_SUCCESS[$strategy]}}")
      break
    fi
  done

  # Print efficiency table
  for strategy in "${STRATEGY_LIST[@]}"; do
    n=${STRAT_RUNS[$strategy]}
    if ((n > 0)); then
      s=${STRAT_SUCCESS[$strategy]}

      pass_pct=$(awk "BEGIN {printf \"%.0f%%\", ($s / $n) * 100}")
      avg_turns=$((STRAT_TURNS[$strategy] / n))
      avg_input=$((STRAT_INPUT[$strategy] / n))

      if ((s > 0)); then
        cps=$(awk "BEGIN {printf \"%.4f\", ${STRAT_COST[$strategy]} / $s}")
        if [[ -n "$baseline_cps" ]]; then
          relative=$(awk "BEGIN {printf \"%.2fx\", $cps / $baseline_cps}")
        else
          relative="1.00x"
        fi
      else
        cps="∞"
        relative="-"
      fi

      printf "%-12s %-8s %-8s %-10s %-12s $%-11s %-10s\n" \
        "${strategy:0:12}" "$n" "$pass_pct" "$avg_turns" "$avg_input" "$cps" "$relative"
    fi
  done

  echo ""

  # === Run Quality Summary ===
  echo "=== Run Quality Summary ==="
  valid_count=0
  invalid_count=0
  declare -A INVALID_CLASSES
  for result in "${RESULTS[@]}"; do
    if [[ -f "$result" ]]; then
      run_status=$(jq -r '.run_status // "valid"' "$result")
      if [[ "$run_status" == "valid" ]]; then
        ((valid_count++)) || true
      else
        ((invalid_count++)) || true
        failure_class=$(jq -r '.failure_class // "UNKNOWN"' "$result")
        ((INVALID_CLASSES[$failure_class]++)) || true
      fi
    fi
  done

  echo "Total runs: ${#RESULTS[@]}"
  echo "Valid runs: $valid_count ($(awk "BEGIN {printf \"%.0f\", ($valid_count / ${#RESULTS[@]}) * 100}")%)"
  if ((invalid_count > 0)); then
    echo "Invalid runs: $invalid_count"
    for class in "${!INVALID_CLASSES[@]}"; do
      echo "  - $class: ${INVALID_CLASSES[$class]}"
    done
  fi

  echo ""

  # Export full JSON
  echo "Full results exported to: $BENCH_DIR/results/matrix-$(date +%Y%m%d-%H%M%S).json"
  jq -s '.' "${RESULTS[@]}" > "$BENCH_DIR/results/matrix-$(date +%Y%m%d-%H%M%S).json" 2>/dev/null
fi
