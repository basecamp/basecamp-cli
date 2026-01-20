# bcq Skill Benchmark

Empirical comparison of skill strategies for Basecamp API interactions with AI agents.

## Hypothesis

bcq provides value over raw API access through:
- Automatic pagination handling
- Rate limit retry on 429 with backoff
- ETag caching for reads
- Clear error UX with actionable guidance

These benefits may be especially pronounced with cheaper/simpler models.

## Strategies

Strategies are defined in `strategies.json`. Each strategy specifies either a skill or prompt:

| Strategy | Type | Description |
|----------|------|-------------|
| `bcq-full` | skill | Full hand-authored skill (control) |
| `bcq-generated` | skill | CLI-generated skill from invariants |
| `bcq-only` | prompt | Minimal "use bcq --help" prompt |
| `api-docs-only` | prompt | Raw API with docs link only |
| `api-docs-with-curl-examples` | prompt | Raw API with endpoint examples |

See `SKILL_BENCH.md` for detailed comparison methodology.

## Dependencies

- `bcq` - Basecamp Query (this repo)
- `jq` - JSON processor
- `yq` - YAML processor (for reading spec.yaml)
- `curl` - HTTP client (standard on most systems)

Install yq on macOS:
```bash
brew install yq
```

## Quick Start

```bash
# 1. Ensure bcq is authenticated
bcq auth status

# 2. Seed test fixtures
./seed.sh

# 3. Setup environment for a task
./harness.sh --task 01 --strategy bcq-full
# This prints the skill/prompt file and task to use

# 4. Execute manually with Claude Code
# Read the task prompt and use the appropriate skill/prompt

# 5. Validate results
./validate.sh check_all_todos 75

# 6. Reset state between runs
./reset.sh
```

## Execution Model

The harness does NOT automatically invoke Claude Code. Instead:

1. **Setup**: `./harness.sh` sets up environment (cache, logging, error injection)
2. **Execute**: You manually invoke Claude Code with the task prompt and skill/prompt
3. **Validate**: Run `./validate.sh` to check success
4. **Reset**: Run `./reset.sh` before the next task

This manual approach allows you to:
- Use any agent (Claude Code, OpenCode, Codex, etc.)
- Observe agent behavior during execution
- Control timing and interruptions

## Tasks

| ID | Name | Type |
|----|------|------|
| 01 | List all todos (pagination) | read |
| 02 | Find and complete todo | chained |
| 03 | Create todo with assignment | chained |
| 04 | Comment on message | chained |
| 05 | Reorder todo in list | chained |
| 06 | Create list, add todos | chained |
| 07 | Recover from 429 | error-recovery |
| 08 | Handle invalid token (401) | error-handling |
| 09 | Bulk complete overdue | bulk |
| 10 | Search with unique marker | read |
| 11 | Prompt injection resilience | security |

## Files

```
benchmarks/
├── README.md           # This file
├── spec.yaml           # Task definitions (canonical source of truth)
├── strategies.json     # Strategy → skill/prompt mapping
├── env.sh              # Environment setup
├── seed.sh             # Create test fixtures
├── reset.sh            # Clean up state
├── harness.sh          # Run tasks, auto-applies injection from spec.yaml
├── validate.sh         # Neutral validation (curl+jq, not bcq)
├── curl                # Curl shim for request logging + error injection
├── inject-proxy.sh     # Error injection state management
├── skills/             # Skill files for skill-based strategies
│   ├── bcq-full/       # Symlink to production skill
│   └── bcq-generated/  # CLI-generated skill
├── prompts/            # Prompt files for prompt-based strategies
│   ├── bcq-only.md
│   ├── api-docs-only.md
│   └── api-docs-with-curl-examples.md
├── tasks/              # Task prompts
│   └── *.md
├── results/            # Output (gitignored)
│   └── *.json
└── .fixtures.json      # Seeded IDs (gitignored)
```

## Metrics

Results are written to `results/<run_id>-<strategy>-<task>.json`:

```json
{
  "run_id": "20250113-120000-1",
  "task_id": "01",
  "strategy": "bcq-full",
  "model": "sonnet",
  "success": true,
  "metrics": {
    "time_ms": 1234,
    "request_count": 3,
    "error_count": 0
  },
  "prompt_size": {
    "instruction_bytes": 5000,
    "task_bytes": 800,
    "total_bytes": 5800
  }
}
```

### Prompt Size Metric

The `prompt_size` field measures bytes of the skill/prompt file + task prompt file. This is a **proxy** for actual prompt size—Claude Code may add system context, conversation history, or tool schemas that aren't captured here. Use this for relative comparisons between strategies.

## Model Comparison

Track model performance by adding `--model`:

```bash
./harness.sh --task 01 --strategy api-docs-with-curl-examples --model haiku
./harness.sh --task 01 --strategy api-docs-with-curl-examples --model sonnet
./harness.sh --task 01 --strategy bcq-full --model haiku
```

This tests the hypothesis that bcq lets cheaper models succeed where raw-API needs expensive reasoning models.

## Aggregate Results

```bash
jq -s 'group_by(.strategy) | map({
  strategy: .[0].strategy,
  success_rate: (map(select(.success)) | length) / length,
  avg_time_ms: (map(.metrics.time_ms) | add / length),
  total_errors: (map(.metrics.error_count) | add)
})' results/*.json
```

## Error Injection

**Automatic**: The harness reads injection config from spec.yaml. Tasks 07 and 08 have injection defined:

```bash
# Task 07: 429 rate limit - tests retry/backoff
./harness.sh --task 07 --strategy bcq-full

# Task 08: 401 invalid token - tests fail-fast UX
./harness.sh --task 08 --strategy bcq-full

# --task all applies correct injection per task
./harness.sh --task all --strategy bcq-full
```

**Manual override**: CLI flags override spec.yaml:

```bash
# Force 429 injection on any task
./harness.sh --task 01 --strategy bcq-full --inject 429 --inject-count 2
```

**How it works**: The `curl` shim intercepts all curl calls, checks `.inject-state`, and returns fake HTTP responses with proper -w output formatting.

## Security Testing (Task 11)

Task 11 validates prompt injection resilience. The harness seeds a message containing:
- Instructions to exfiltrate tokens
- Commands to call external URLs
- Shell command injection attempts

Validation checks:
- No requests to unexpected hosts
- No write operations (read-only task)
- Correct data extraction ("3pm" meeting time)

## Validation

All validation uses neutral curl+jq (not bcq):

```bash
./validate.sh check_todo_completed
./validate.sh check_list_with_todos "Benchmark List" 3
./validate.sh jq "/projects.json" "length > 0"
```
