#!/usr/bin/env bash
# race-shard-union-test.sh — the union check's own tests.
#
# The union check is the reason it is safe to shard this job at all, so it
# is worth knowing it fails. Each case below is a way the shards could
# silently stop covering the suite; all of them must be refused.
set -euo pipefail
cd "$(dirname "$0")/.."
union=scripts/race-shard-union.sh

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

plant() {
  rm -rf "${work:?}"/*
  for i in 1 2 3 4; do
    mkdir -p "$work/shard-$i"
    printf 'pkg/a\tTestOne\npkg/a\tTestTwo\npkg/b\tTestThree\npkg/b\tTestFour\n' > "$work/shard-$i/all.txt"
  done
  printf 'pkg/a\tTestOne\n'   > "$work/shard-1/shard.1.txt"
  printf 'pkg/a\tTestTwo\n'   > "$work/shard-2/shard.2.txt"
  printf 'pkg/b\tTestThree\n' > "$work/shard-3/shard.3.txt"
  printf 'pkg/b\tTestFour\n'  > "$work/shard-4/shard.4.txt"
}

refuses() {
  local what="$1"
  if "$union" "$work" 4 >/dev/null 2>&1; then
    echo "FAIL: the union check accepted $what" >&2
    exit 1
  fi
  echo "ok - refuses $what"
}

plant
"$union" "$work" 4 >/dev/null || { echo "FAIL: the union check refused a complete, disjoint split" >&2; exit 1; }
echo "ok - accepts a complete, disjoint split"

plant; : > "$work/shard-3/shard.3.txt"
refuses "a shard assigned nothing"

plant; rm "$work/shard-2/shard.2.txt"
refuses "a shard that recorded no assignment"

plant; printf 'pkg/a\tTestOne\n' >> "$work/shard-3/shard.3.txt"
refuses "the same test assigned to two shards"

plant; printf 'pkg/b\tTestFive\n' >> "$work/shard-1/all.txt"
refuses "a test in the enumeration and in no shard"

plant; printf 'pkg/b\tTestFive\n' >> "$work/shard-2/all.txt"
refuses "shards that enumerated different sets of tests"

plant; printf 'pkg/z\tTestGhost\n' >> "$work/shard-4/shard.4.txt"
refuses "a test assigned that the repository does not have"

plant; : > "$work/shard-1/all.txt"
refuses "a shard whose enumeration is empty"

echo "All union checks behaved."
