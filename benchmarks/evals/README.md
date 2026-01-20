# Basecamp API Evals

Eval harness for testing AI agent behavior against Basecamp API workflows.

## Quick Start

```bash
# Set OpenAI API key
export OPENAI_API_KEY="sk-..."

# Run single case
ruby harness/agent_runner.rb pagination api-docs-with-agent-invariants -m gpt-4o

# Run all cases (positional args: model, iterations, prompt)
./run_all.sh gpt-4o 10
```

## Exit Codes

| Code | Meaning | Aggregation |
|------|---------|-------------|
| 0 | PASS | Count toward pass rate |
| 1 | FAIL | Count toward pass rate |
| 2 | SKIP | **Exclude from pass rate** (infrastructure error) |

**Important**: Exit code 2 indicates infrastructure failure (API timeout, auth error, network issue) — not a model failure. Aggregation scripts MUST exclude these from pass rate calculations.

Example aggregation:
```bash
passes=0; fails=0; skips=0
for case in cases/*.yml; do
  ruby harness/agent_runner.rb "$case" api-docs-with-agent-invariants -m gpt-4o
  case $? in
    0) ((passes++)) ;;
    1) ((fails++)) ;;
    2) ((skips++)) ;;  # Don't count in pass rate
  esac
done
echo "Pass rate: $passes / $((passes + fails)) (excluding $skips skipped)"
```

## Cases

**Note**: Both infrastructure and model tests are counted in the overall pass rate. Infrastructure tests validate that helpers work correctly; model tests validate agent reasoning. When reporting results, consider separating these categories if you need to isolate model capability.

### Infrastructure-Level Tests

These test the **pagination helper** behavior, not model behavior. The model uses `paginate: true` and never sees the underlying complexity.

| Case | Tests |
|------|-------|
| `pagination.yml` | Helper correctly fetches all pages until empty |
| `retry_429.yml` | Helper correctly retries on 429 during pagination |

### Model-Level Tests

These test **model behavior** directly. The model must recognize and handle the situation.

| Case | Tests |
|------|-------|
| `retry_429_nonpaginated.yml` | Model handles 429 on single-resource endpoint |
| `recover_422.yml` | Model reads error hint and retries with correct body |
| `comment_idempotency.yml` | Model checks existing comments before posting |

## Metrics

The harness tracks helper-level metrics separately from model behavior:

```
Helper metrics (pagination/429 handled by infrastructure):
  retries_used: 1
  pages_fetched: 6
```

These appear in output when relevant. They indicate infrastructure work, not model "skill."

## Adding Cases

See `CASE_SCHEMA.md` for the case definition format.

Key fields:
- `fixtures`: Stub server responses
- `inject`: Error injection (429, 422, etc.) on specific call numbers
- `assertions`: What must happen (required_sequence, forbidden, end_state, max_calls)

## Files

```
evals/
├── cases/                    # Test case definitions
│   ├── pagination.yml        # Infrastructure: pagination helper
│   ├── retry_429.yml         # Infrastructure: 429 retry in helper
│   ├── retry_429_nonpaginated.yml  # Model: 429 recovery
│   ├── recover_422.yml       # Model: 422 recovery
│   └── comment_idempotency.yml     # Model: idempotent writes
├── harness/
│   ├── agent_runner.rb       # LLM agent integration
│   ├── runner.rb             # Test runner core
│   ├── stub_server.rb        # Fixture-based HTTP stub
│   ├── assertions.rb         # Assertion checking
│   ├── case_loader.rb        # YAML parsing + validation
│   └── normalizer.rb         # URL normalization
├── CASE_SCHEMA.md            # Case file format spec
└── README.md                 # This file
```
