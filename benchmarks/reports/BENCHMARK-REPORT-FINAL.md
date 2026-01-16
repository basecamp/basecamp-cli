# Task 12 Benchmark Report

> **Cohort**: `baseline_soft_anchor_env_today` (N=5 per cell)
> **Generated**: 2026-01-16

## Reliability

| Model | bcq | raw |
|-------|-----|-----|
| **claude-sonnet** | 5/5 (100%) | 5/5 (100%) |
| **claude-haiku** | 5/5 (100%) | 5/5 (100%) |
| **gpt-5-mini** | 5/5 (100%) | 0/5 (0%) |
| **gpt-5-nano** | 5/5 (100%) | 0/5 (0%) |

## Efficiency

| Model | bcq turns | raw turns | bcq $/success | raw $/success |
|-------|-----------|-----------|---------------|---------------|
| **claude-sonnet** | 2.0 | 10.0 | $0.016 | $0.26 |
| **claude-haiku** | 3.0 | 24.6 | $0.008 | $0.56 |
| **gpt-5-mini** | 3.0 | — | $0.005 | — |
| **gpt-5-nano** | 2.8 | — | $0.001 | — |

**bcq is 16× cheaper for Sonnet, 70× cheaper for Haiku.**

## Analysis

- **Strong-model parity on reliability**: Sonnet and Haiku complete the raw traversal reliably on this task—but at 5–12× the turn count and 16–70× the cost.

- **Cheap-model differential**: GPT-5-mini and GPT-5-nano fail raw consistently (0/5) while bcq succeeds 100%. Root cause: wrong dock ID selection (message_board ID instead of todoset ID) → 404s.

## Conclusion

bcq unlocks reliability for cheaper models and dramatically improves efficiency for all models. Even when raw succeeds, bcq's encapsulation of API navigation complexity (dock parsing, ID selection, pagination) yields 16–70× cost reduction.
