# Run Quality Gate Design

## Overview

Every benchmark run must be automatically classified as valid or invalid before its results count toward aggregate metrics. This eliminates manual forensics and ensures result credibility.

## Failure Taxonomy

```
VALID              - Run completed, model had fair chance, result is meaningful
HARNESS_BUG        - Our fault: wrong prompt, missing env, injection didn't fire
INFRA_FLAKE        - Transient: 5xx storms, auth expired, network failures
MODEL_FAILURE      - Model's fault: no tool use, wrong syntax, incomplete execution
DATA_ISSUE         - Fixture problem: missing todoset, wrong IDs, inconsistent state
API_UNAVAILABLE    - Model API returned error, 0 tokens, immediate failure
```

## Detection Rules

### HARNESS_BUG (rerun after fix)
```bash
# Detection signals:
- tools.jsonl contains "command not found" or "env: not set"
- BCQ_BENCH_* variable referenced but empty in tool output
- Injection configured but no 429 in http.jsonl AND no page=2 request
- Single quotes around $BCQ_BENCH_RUN_ID in tool input (quote escaping bug in prompt)
- validation.txt contains "BCQ_BENCH_RUN_ID not set"
```

### INFRA_FLAKE (auto-rerun)
```bash
# Detection signals:
- http.jsonl contains 5xx responses (not injected)
- api_responses.jsonl contains "overloaded" or "rate_limit" from Anthropic/OpenAI
- Token refresh failed mid-run (401 after initial success)
- Network timeout patterns
```

### MODEL_FAILURE (valid failure - counts against model)
```bash
# Detection signals:
- tools.jsonl shows tool calls but zero HTTP requests to Basecamp
- Tool output contains code blocks but no execution (model printed instead of ran)
- Pagination incomplete: started but didn't finish all pages
- Marker comment missing or wrong content (model error, not env error)
- updated_at timestamps show no changes during run window
```

### DATA_ISSUE (rerun after fixture fix)
```bash
# Detection signals:
- validation.txt contains "Could not fetch" for expected fixture IDs
- Todoset/todolist not found (404) for seeded IDs
- Fixture count mismatch (expected 3 overdue, found 0)
```

### API_UNAVAILABLE (exclude from analysis)
```bash
# Detection signals:
- metrics.json shows tokens.total == 0
- metrics.json shows turns == 1 AND tools.calls == 0
- api_responses.jsonl contains immediate error response
- Model ID not recognized or deprecated
```

## Preflight Checks (run.sh)

Before starting agent:
```bash
preflight_check() {
  local errors=()

  # Environment
  [[ -z "$BASECAMP_TOKEN" ]] && errors+=("BASECAMP_TOKEN not set")
  [[ -z "$BCQ_ACCOUNT_ID" ]] && errors+=("BCQ_ACCOUNT_ID not set")
  [[ -z "$BCQ_BENCH_RUN_ID" ]] && errors+=("BCQ_BENCH_RUN_ID not set")

  # Model availability (quick API ping)
  if ! check_model_available "$MODEL"; then
    errors+=("Model $MODEL not available")
  fi

  # Fixtures exist
  if ! verify_fixtures "$TASK"; then
    errors+=("Fixtures for task $TASK not valid")
  fi

  # Injection sanity
  if [[ -n "$BCQ_INJECT_ERROR" ]] && [[ -z "$BCQ_INJECT_MATCH" ]]; then
    errors+=("Injection configured but no match pattern")
  fi

  if [[ ${#errors[@]} -gt 0 ]]; then
    log_error "Preflight failed:"
    printf '  - %s\n' "${errors[@]}"
    return 1
  fi
  return 0
}
```

## In-Run Invariants

Captured during execution:
```bash
# Track in run metadata
in_run_signals() {
  local signals=()

  # Tool usage
  local tool_calls=$(jq -s 'length' "$LOG_DIR/tools.jsonl")
  signals+=("tool_calls=$tool_calls")

  # HTTP activity
  local http_requests=$(wc -l < "$LOG_DIR/http.jsonl")
  signals+=("http_requests=$http_requests")

  # Errors in tool output
  local tool_errors=$(grep -c "error\|Error\|ERROR" "$LOG_DIR/tools.jsonl" || echo 0)
  signals+=("tool_errors=$tool_errors")

  # Env var issues
  local env_issues=$(grep -c "not set\|undefined\|empty" "$LOG_DIR/tools.jsonl" || echo 0)
  signals+=("env_issues=$env_issues")

  echo "${signals[*]}"
}
```

## Post-Run Triage (triage.sh)

```bash
#!/usr/bin/env bash
# benchmarks/harness/triage.sh
# Classifies run failures and determines rerun eligibility

triage_run() {
  local run_dir="$1"
  local metrics="$run_dir/metrics.json"
  local tools="$run_dir/tools.jsonl"
  local http="$run_dir/http.jsonl"
  local validation="$run_dir/validation.txt"
  local api="$run_dir/api_responses.jsonl"

  # Already passed? Valid success.
  if jq -e '.success == true' "$metrics" >/dev/null 2>&1; then
    echo "VALID:success"
    return 0
  fi

  # Check API_UNAVAILABLE first (most obvious)
  local tokens=$(jq -r '.metrics.tokens.total // 0' "$metrics")
  local turns=$(jq -r '.metrics.turns // 0' "$metrics")
  local tool_calls=$(jq -r '.metrics.tools.calls // 0' "$metrics")

  if [[ "$tokens" -eq 0 ]] || { [[ "$turns" -eq 1 ]] && [[ "$tool_calls" -eq 0 ]]; }; then
    echo "API_UNAVAILABLE:zero_tokens_or_no_tools"
    return 1
  fi

  # Check HARNESS_BUG patterns
  if grep -q "not set\|command not found" "$tools" 2>/dev/null; then
    echo "HARNESS_BUG:env_or_command_missing"
    return 1
  fi

  # Check for quote escaping bug (single quotes around variables)
  if grep -q "'\$BCQ_BENCH" "$tools" 2>/dev/null; then
    echo "HARNESS_BUG:single_quote_no_expansion"
    return 1
  fi

  # Check injection didn't fire when configured
  if [[ -n "${BCQ_BENCH_INJECTION_EXPECTED:-}" ]]; then
    if ! grep -q '"http_code":"429"' "$http" 2>/dev/null; then
      # Check if the triggering request even happened
      if ! grep -q "page=2" "$http" 2>/dev/null; then
        echo "HARNESS_BUG:injection_not_triggered_no_page2"
        return 1
      fi
    fi
  fi

  # Check INFRA_FLAKE patterns
  if grep -qE '"http_code":"5[0-9]{2}"' "$http" 2>/dev/null; then
    local injected_5xx=$(grep -c '"injected":true.*"http_code":"5' "$http" || echo 0)
    local total_5xx=$(grep -cE '"http_code":"5[0-9]{2}"' "$http" || echo 0)
    if [[ $((total_5xx - injected_5xx)) -gt 0 ]]; then
      echo "INFRA_FLAKE:server_5xx"
      return 1
    fi
  fi

  # Check DATA_ISSUE patterns
  if grep -q "Could not fetch\|404\|not found" "$validation" 2>/dev/null; then
    echo "DATA_ISSUE:fixture_not_found"
    return 1
  fi

  # If we get here, it's a MODEL_FAILURE (valid failure)
  # Determine specific failure mode for reporting
  local failure_mode="unknown"

  if grep -q "Completed: 0/" "$validation" 2>/dev/null; then
    failure_mode="no_completions"
  elif grep -q "Commented: 0/" "$validation" 2>/dev/null; then
    failure_mode="no_comments"
  elif grep -q "missing marker" "$validation" 2>/dev/null; then
    failure_mode="wrong_marker_content"
  elif grep -q "not after run_start" "$validation" 2>/dev/null; then
    failure_mode="stale_data"
  fi

  echo "MODEL_FAILURE:$failure_mode"
  return 0  # Valid failure - counts against model
}
```

## Metrics Schema Update

```json
{
  "run_id": "20260115-190436-12-bcq-full-gpt-5-mini",
  "task": "12",
  "strategy": "bcq-full",
  "model": "gpt-5-mini",
  "success": false,
  "run_status": "valid",           // NEW: valid | invalid
  "failure_class": "MODEL_FAILURE", // NEW: taxonomy classification
  "failure_detail": "wrong_marker_content", // NEW: specific reason
  "rerun_recommended": false,      // NEW: should this be rerun?
  "metrics": { ... }
}
```

## Matrix Integration

```bash
# matrix.sh additions

# After each run, triage it
triage_result=$(./harness/triage.sh "$LOG_DIR")
failure_class="${triage_result%%:*}"
failure_detail="${triage_result#*:}"

# Update metrics with triage info
jq --arg class "$failure_class" \
   --arg detail "$failure_detail" \
   --arg status "$(is_valid_class "$failure_class" && echo valid || echo invalid)" \
   --argjson rerun "$(should_rerun "$failure_class" && echo true || echo false)" \
   '. + {run_status: $status, failure_class: $class, failure_detail: $detail, rerun_recommended: $rerun}' \
   "$LOG_DIR/metrics.json" > "$LOG_DIR/metrics.json.tmp" && \
   mv "$LOG_DIR/metrics.json.tmp" "$LOG_DIR/metrics.json"

# --rerun-invalid N flag
if [[ "$RERUN_INVALID" -gt 0 ]] && should_rerun "$failure_class"; then
  log_info "Rerunning invalid run (class=$failure_class, attempt=$rerun_attempt)"
  # Decrement rerun counter and retry
fi
```

## Rerun Policy

| Failure Class | Rerun? | Condition |
|---------------|--------|-----------|
| HARNESS_BUG | Yes | After fixing the bug |
| INFRA_FLAKE | Yes | Auto-rerun up to 2x |
| MODEL_FAILURE | No | Valid result |
| DATA_ISSUE | Yes | After fixing fixtures |
| API_UNAVAILABLE | Maybe | If transient, rerun; if model deprecated, exclude |

## Report Output

```
=== Run Quality Summary ===
Total runs: 40
Valid runs: 32 (80%)
Invalid runs: 8
  - HARNESS_BUG: 5 (single_quote_no_expansion)
  - API_UNAVAILABLE: 3 (zero_tokens)

=== Valid Results Only ===
| Condition | Model | Valid Runs | Pass% | ... |
```

## Implementation Order

1. Create `harness/triage.sh` with classification logic
2. Add preflight checks to `run.sh`
3. Update `run.sh` to call triage and record metadata
4. Add `--rerun-invalid N` to `matrix.sh`
5. Update report generation to filter by run_status
