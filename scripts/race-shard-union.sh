#!/usr/bin/env bash
# race-shard-union.sh - the aggregate job's check that the shards between
# them ran every test, once.
#
# This is the point of sharding the race job rather than a nicety attached to
# it. A split that silently stops running some tests reports a fast green and
# measures nothing, which is the defect this repository has spent a lot of
# effort closing elsewhere. So every one of these is a failure, not a warning:
#
#   - a shard that enumerated a different set of tests than its siblings,
#     which means they did not all see the same repository
#   - the same test assigned to two shards, which means the split is wrong
#     even where it looks complete
#   - a test in no shard at all, which is the one that matters
#
#   race-shard-union.sh <directory of shard records> <number of shards>
set -euo pipefail

dir="${1:?directory holding the shard records}"
total="${2:?number of shards}"

fail() { echo "SHARD COVERAGE FAILED: $*" >&2; exit 1; }

sorted() { LC_ALL=C sort -u "$1"; }

# Compared as sorted files rather than hashed: cmp is POSIX and md5sum is
# not on macOS, and this check is in make check, which people run locally.
for i in $(seq 1 "$total"); do
  [ -s "$dir/shard-$i/all.txt" ] || fail "shard $i recorded no enumeration; either it did not get far enough to have one, or its report did not reach here"
  sorted "$dir/shard-$i/all.txt" > "$dir/all.$i.sorted"
  if [ "$i" != 1 ] && ! cmp -s "$dir/all.1.sorted" "$dir/all.$i.sorted"; then
    fail "shard $i enumerated a different set of tests than shard 1, so the shards did not all see the same repository"
  fi
done

for i in $(seq 1 "$total"); do
  [ -s "$dir/shard-$i/shard.$i.txt" ] || fail "shard $i was assigned no tests"
  for j in $(seq $((i + 1)) "$total"); do
    overlap=$(LC_ALL=C comm -12 <(sorted "$dir/shard-$i/shard.$i.txt") <(sorted "$dir/shard-$j/shard.$j.txt"))
    [ -z "$overlap" ] || fail "shards $i and $j were both assigned $(echo "$overlap" | wc -l) test(s), e.g. $(echo "$overlap" | head -1)"
  done
done

cat "$dir"/shard-*/shard.*.txt | LC_ALL=C sort -u > "$dir/union.txt"
cp "$dir/all.1.sorted" "$dir/expected.txt"

missing=$(LC_ALL=C comm -23 "$dir/expected.txt" "$dir/union.txt")
[ -z "$missing" ] || fail "$(echo "$missing" | wc -l) test(s) were in no shard and so did not run, e.g. $(echo "$missing" | head -1)"

extra=$(LC_ALL=C comm -13 "$dir/expected.txt" "$dir/union.txt")
[ -z "$extra" ] || fail "$(echo "$extra" | wc -l) test(s) were assigned that the repository does not have, e.g. $(echo "$extra" | head -1)"

echo "shard coverage OK: $(wc -l < "$dir/expected.txt") tests across $total shards, disjoint and complete"
