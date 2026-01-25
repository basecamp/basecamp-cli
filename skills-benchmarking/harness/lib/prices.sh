#!/usr/bin/env bash
# Token pricing per model (USD per 1M tokens)
# Updated: 2026-01-14
# Sources: https://openai.com/api/pricing/ https://www.anthropic.com/pricing

# Requires bash 4.0+ for associative arrays
if ((BASH_VERSINFO[0] < 4)); then
  echo "Error: bash 4.0+ required for prices.sh" >&2
  exit 1
fi

declare -A PRICE_INPUT=(
  # Anthropic Claude 4.5 (current)
  ["claude-opus-4.5"]=5.00
  ["claude-opus-4-5-20251101"]=5.00
  ["claude-sonnet-4.5"]=3.00
  ["claude-sonnet-4-5-20250929"]=3.00
  ["claude-haiku-4.5"]=1.00
  ["claude-haiku-4-5-20251001"]=1.00
  # Anthropic Claude 4 (legacy)
  ["claude-opus-4"]=15.00
  ["claude-opus-4-20250514"]=15.00
  ["claude-opus-4.1"]=15.00
  ["claude-opus-4-1-20250805"]=15.00
  ["claude-sonnet-4"]=3.00
  ["claude-sonnet-4-20250514"]=3.00
  ["claude-3-5-haiku-20241022"]=0.80
  # OpenAI GPT-5 series (current)
  ["gpt-5.2"]=1.75
  ["gpt-5.2-pro"]=21.00
  ["gpt-5.1"]=1.25
  ["gpt-5"]=1.25
  ["gpt-5-mini"]=0.25
  ["gpt-5-nano"]=0.05
  # OpenAI o-series
  ["o3"]=2.00
  ["o3-mini"]=1.10
  ["o3-pro"]=20.00
  ["o4-mini"]=1.10
  # OpenAI GPT-4 series (legacy)
  ["gpt-4o"]=2.50
  ["gpt-4o-2024-08-06"]=2.50
  ["gpt-4o-mini"]=0.15
  ["gpt-4o-mini-2024-07-18"]=0.15
  ["o1"]=15.00
  ["o1-mini"]=3.00
  # Aliases
  ["claude-sonnet"]=3.00
  ["claude-haiku"]=1.00
)

declare -A PRICE_OUTPUT=(
  # Anthropic Claude 4.5 (current)
  ["claude-opus-4.5"]=25.00
  ["claude-opus-4-5-20251101"]=25.00
  ["claude-sonnet-4.5"]=15.00
  ["claude-sonnet-4-5-20250929"]=15.00
  ["claude-haiku-4.5"]=5.00
  ["claude-haiku-4-5-20251001"]=5.00
  # Anthropic Claude 4 (legacy)
  ["claude-opus-4"]=75.00
  ["claude-opus-4-20250514"]=75.00
  ["claude-opus-4.1"]=75.00
  ["claude-opus-4-1-20250805"]=75.00
  ["claude-sonnet-4"]=15.00
  ["claude-sonnet-4-20250514"]=15.00
  ["claude-3-5-haiku-20241022"]=4.00
  # OpenAI GPT-5 series (current)
  ["gpt-5.2"]=14.00
  ["gpt-5.2-pro"]=168.00
  ["gpt-5.1"]=10.00
  ["gpt-5"]=10.00
  ["gpt-5-mini"]=2.00
  ["gpt-5-nano"]=0.40
  # OpenAI o-series
  ["o3"]=8.00
  ["o3-mini"]=4.40
  ["o3-pro"]=80.00
  ["o4-mini"]=4.40
  # OpenAI GPT-4 series (legacy)
  ["gpt-4o"]=10.00
  ["gpt-4o-2024-08-06"]=10.00
  ["gpt-4o-mini"]=0.60
  ["gpt-4o-mini-2024-07-18"]=0.60
  ["o1"]=60.00
  ["o1-mini"]=12.00
  # Aliases
  ["claude-sonnet"]=15.00
  ["claude-haiku"]=5.00
)

# Cache pricing (per 1M tokens)
# OpenAI: 90% off input for cached tokens
# Anthropic: ~10% of input for read, ~125% of input for write
declare -A PRICE_CACHE_WRITE=(
  # Anthropic Claude 4.5
  ["claude-opus-4.5"]=6.25
  ["claude-opus-4-5-20251101"]=6.25
  ["claude-sonnet-4.5"]=3.75
  ["claude-sonnet-4-5-20250929"]=3.75
  ["claude-haiku-4.5"]=1.25
  ["claude-haiku-4-5-20251001"]=1.25
  # Anthropic Claude 4 (legacy)
  ["claude-opus-4"]=18.75
  ["claude-opus-4-20250514"]=18.75
  ["claude-opus-4.1"]=18.75
  ["claude-opus-4-1-20250805"]=18.75
  ["claude-sonnet-4"]=3.75
  ["claude-sonnet-4-20250514"]=3.75
  ["claude-3-5-haiku-20241022"]=1.00
  # Aliases
  ["claude-sonnet"]=3.75
  ["claude-haiku"]=1.25
)

declare -A PRICE_CACHE_READ=(
  # Anthropic Claude 4.5
  ["claude-opus-4.5"]=0.50
  ["claude-opus-4-5-20251101"]=0.50
  ["claude-sonnet-4.5"]=0.30
  ["claude-sonnet-4-5-20250929"]=0.30
  ["claude-haiku-4.5"]=0.10
  ["claude-haiku-4-5-20251001"]=0.10
  # Anthropic Claude 4 (legacy)
  ["claude-opus-4"]=1.50
  ["claude-opus-4-20250514"]=1.50
  ["claude-opus-4.1"]=1.50
  ["claude-opus-4-1-20250805"]=1.50
  ["claude-sonnet-4"]=0.30
  ["claude-sonnet-4-20250514"]=0.30
  ["claude-3-5-haiku-20241022"]=0.08
  # OpenAI (cached input pricing)
  ["gpt-5.2"]=0.175
  ["gpt-5.2-pro"]=0.00
  ["gpt-5.1"]=0.125
  ["gpt-5"]=0.125
  ["gpt-5-mini"]=0.025
  ["gpt-5-nano"]=0.005
  # OpenAI o-series
  ["o3"]=0.50
  ["o3-mini"]=0.55
  ["o3-pro"]=0.00
  ["o4-mini"]=0.275
  # Aliases
  ["claude-sonnet"]=0.30
  ["claude-haiku"]=0.10
)

# Calculate cost for a run
# Usage: calc_cost <model> <input_tokens> <output_tokens> [cache_read] [cache_write]
# Returns: cost in USD (float)
calc_cost() {
  local model="$1"
  local input_tokens="${2:-0}"
  local output_tokens="${3:-0}"
  local cache_read="${4:-0}"
  local cache_write="${5:-0}"

  local price_in="${PRICE_INPUT[$model]:-0}"
  local price_out="${PRICE_OUTPUT[$model]:-0}"
  local price_cache_read="${PRICE_CACHE_READ[$model]:-0}"
  local price_cache_write="${PRICE_CACHE_WRITE[$model]:-0}"

  # Cost = (tokens * price_per_million) / 1,000,000
  # Use awk for floating point
  awk -v in_t="$input_tokens" -v out_t="$output_tokens" \
      -v cache_r="$cache_read" -v cache_w="$cache_write" \
      -v p_in="$price_in" -v p_out="$price_out" \
      -v p_cr="$price_cache_read" -v p_cw="$price_cache_write" \
      'BEGIN {
        cost = (in_t * p_in + out_t * p_out + cache_r * p_cr + cache_w * p_cw) / 1000000
        printf "%.6f", cost
      }'
}

# Get price for a model (for display)
# Usage: get_price <model> <input|output|cache_read|cache_write>
get_price() {
  local model="$1"
  local type="$2"

  case "$type" in
    input) echo "${PRICE_INPUT[$model]:-0}" ;;
    output) echo "${PRICE_OUTPUT[$model]:-0}" ;;
    cache_read) echo "${PRICE_CACHE_READ[$model]:-0}" ;;
    cache_write) echo "${PRICE_CACHE_WRITE[$model]:-0}" ;;
    *) echo "0" ;;
  esac
}
