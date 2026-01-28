#!/usr/bin/env bash
# Test runner for bcq
# Runs the bats test suite

set -euo pipefail

# Disable keyring for headless testing (Go binary)
export BCQ_NO_KEYRING=1

cd "$(dirname "$0")"

if ! command -v bats &>/dev/null; then
  echo "Error: bats not found. Install with your package manager (e.g., pacman -S bats, brew install bats-core)" >&2
  exit 1
fi

# Auto-detect CPU cores for parallel execution
jobs=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 1)

# Use rush (macOS) or parallel (Linux) for parallelization
if command -v rush &>/dev/null; then
  exec bats --parallel-binary-name rush -j "$jobs" "$@" *.bats
elif command -v parallel &>/dev/null && parallel --version 2>&1 | grep -q "GNU"; then
  exec bats -j "$jobs" "$@" *.bats
else
  exec bats "$@" *.bats
fi
