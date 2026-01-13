#!/usr/bin/env bash
# Test runner for bcq
# Runs the bats test suite

set -euo pipefail
cd "$(dirname "$0")"

if ! command -v bats &>/dev/null; then
  echo "Error: bats not found. Install with: apt install bats (Linux) or brew install bats-core (macOS)" >&2
  exit 1
fi

exec bats "$@" *.bats
