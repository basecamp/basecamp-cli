# BCQ Benchmark Harness

Model-agnostic agent runner with full instrumentation for comparing bcq vs raw API approaches.

## Quick Start

```bash
# Set API keys
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."

# Run single benchmark
./harness/run.sh --task 12 --strategy bcq-full --model claude-sonnet

# Run full matrix
./harness/matrix.sh --task 12 --models "claude-sonnet,gpt-4o" --strategies "bcq-full,api-docs-with-curl-examples"
```

## Supported Models

| Provider | Models |
|----------|--------|
| Anthropic | `claude-sonnet`, `claude-haiku`, `claude-opus` |
| OpenAI | `gpt-4o`, `gpt-4-turbo`, `o1-mini`, `o3-mini` |

## Output

Each run produces:
```
results/{run_id}/
├── metrics.json          # Token counts, timing, HTTP requests
├── conversation.jsonl    # Full agent conversation log
├── http.jsonl            # HTTP request log (if curl wrapper used)
├── tools.jsonl           # Tool execution log
└── api_responses.jsonl   # Raw API responses
```

## Metrics Captured

```json
{
  "metrics": {
    "duration_ms": 45230,
    "turns": 8,
    "tokens": {
      "input": 12450,
      "output": 3200,
      "cache_read": 8000,
      "cache_write": 4450,
      "total": 15650
    },
    "api": {
      "calls": 8,
      "latency_ms": 12500
    },
    "tools": {
      "calls": 14,
      "errors": 0
    },
    "http": {
      "requests": 24,
      "rate_limited": 1
    },
    "prompt": {
      "system_bytes": 2400,
      "task_bytes": 1800,
      "total_bytes": 4200
    }
  }
}
```

## Adding New Models

Create adapter in `lib/models/{provider}.sh` implementing:
- `model_init(model_name)` - Initialize API client
- `model_call(system, messages_json)` - Send request, return response
- `model_parse_tools(response)` - Extract tool calls as JSON array
- `model_stop_reason(response)` - Extract stop reason
- `model_metrics(response)` - Update METRICS array with token counts
- `model_format_assistant(response)` - Format for message history
- `model_format_tool_result(id, name, result, exit_code)` - Format tool result

## Architecture

```
harness/
├── run.sh                    # Main entry point
├── matrix.sh                 # Multi-run comparison
└── lib/
    ├── core.sh               # Agentic loop, tool dispatch
    ├── logging.sh            # Structured logging, metrics
    └── models/
        ├── anthropic.sh      # Claude adapter
        └── openai.sh         # GPT adapter
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ANTHROPIC_API_KEY` | For Claude | Anthropic API key |
| `OPENAI_API_KEY` | For GPT | OpenAI API key |
| `LOG_LEVEL` | No | debug, info, warn, error (default: info) |
| `ANTHROPIC_THINKING` | No | Enable extended thinking for Opus |
