#!/bin/bash
# Collect CPU profile from benchmarks for Profile-Guided Optimization (PGO)
#
# Usage:
#   ./scripts/collect-profile.sh [output_dir]
#
# This script runs Go benchmarks with CPU profiling enabled and generates
# a merged profile suitable for PGO builds. The profile is saved as
# default.pgo in the project root for automatic detection by `go build -pgo=auto`.
#
# Requirements:
#   - Go 1.21+ (for PGO support)
#   - go tool pprof

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROFILE_DIR="${1:-$PROJECT_ROOT/profiles}"

cd "$PROJECT_ROOT"

echo "==> Creating profile directory: $PROFILE_DIR"
mkdir -p "$PROFILE_DIR"

echo "==> Running benchmarks with CPU profiling..."
# Go's -cpuprofile doesn't work with multiple packages, so we profile each and merge
# -benchtime=3s gives enough samples while keeping total time reasonable

PACKAGES=$(go list ./internal/...)
PROFILE_FILES=""
i=0

for pkg in $PACKAGES; do
    i=$((i + 1))
    pkg_name=$(basename "$pkg")
    profile_file="$PROFILE_DIR/bench_${pkg_name}.pprof"
    echo "    Profiling $pkg_name..."
    BASECAMP_NO_KEYRING=1 go test -cpuprofile="$profile_file" \
        -bench=. \
        -benchtime=3s \
        -count=1 \
        "$pkg" >/dev/null 2>&1 || true
    if [[ -f "$profile_file" && -s "$profile_file" ]]; then
        PROFILE_FILES="$PROFILE_FILES $profile_file"
    fi
done

echo "==> Merging profiles..."
# Merge all profiles into one using go tool pprof
if [[ -n "$PROFILE_FILES" ]]; then
    go tool pprof -proto $PROFILE_FILES > "$PROFILE_DIR/merged.pprof"
else
    echo "Error: No profiles generated"
    exit 1
fi

echo "==> Converting to PGO format..."
cp "$PROFILE_DIR/merged.pprof" "$PROFILE_DIR/default.pgo"

# Copy to project root for -pgo=auto detection
cp "$PROFILE_DIR/default.pgo" "$PROJECT_ROOT/default.pgo"

echo "==> Profile statistics:"
go tool pprof -top -nodecount=10 "$PROFILE_DIR/merged.pprof" 2>/dev/null | head -20 || true

echo ""
echo "==> Profile saved to: $PROJECT_ROOT/default.pgo"
echo "    Size: $(du -h "$PROJECT_ROOT/default.pgo" | cut -f1)"
echo ""
echo "Build with PGO:"
echo "    go build -pgo=auto ./cmd/basecamp"
echo "    # or"
echo "    make build-pgo"
