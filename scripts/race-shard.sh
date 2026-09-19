#!/usr/bin/env bash
# race-shard.sh - run one shard of the race-detector suite.
#
# Race detection was one job of nineteen minutes, and seventeen of them were
# one package: internal/connector is 1031s of a 1061s critical path. Sharding
# by package therefore buys nothing — whichever shard gets that package still
# takes seventeen minutes — so the unit here is the test, not the package.
#
# The hazard that comes with that is the one this repository keeps finding:
# a shard scheme that silently stops running some tests is green and measures
# nothing. Two checks answer it. This script refuses to report success unless
# every test it was assigned actually ran, and race-shard-union.sh, in the
# aggregate job, refuses unless the shards between them covered the lot.
#
#   race-shard.sh <index> <total> <output directory>
set -euo pipefail

index="${1:?shard index, 1-based}"
total="${2:?number of shards}"
out="${3:?directory to write the shard record into}"
mkdir -p "$out"

all="$out/all.txt"
plan="$out/shard.$index.txt"

# -json, not a bare -list: a plain listing prints test names with no package,
# and six names in this repository exist in more than one. The union check
# downstream compares package-and-test pairs, so the attribution has to be
# unambiguous here.
go test -tags dev -list '.*' -json ./... |
  python3 -c '
import sys, json
for line in sys.stdin:
    try:
        event = json.loads(line)
    except ValueError:
        continue
    if event.get("Action") != "output":
        continue
    name = event.get("Output", "").strip()
    # Benchmarks are listed but -run does not run them without -bench, so
    # assigning one would be assigning work no shard can do. The race job
    # has never run them; it does not start now.
    if name and " " not in name and name.startswith(("Test", "Fuzz", "Example")):
        print(event["Package"] + "\t" + name)
' | LC_ALL=C sort -u > "$all"

[ -s "$all" ] || { echo "enumerated no tests at all" >&2; exit 1; }
awk -v i="$index" -v n="$total" 'NR % n == i % n' "$all" > "$plan"
[ -s "$plan" ] || { echo "shard $index of $total was assigned no tests" >&2; exit 1; }

echo "shard $index of $total: $(wc -l < "$plan") of $(wc -l < "$all") tests"

# One invocation over ./... keeps go test's own package parallelism. A name
# that exists in two packages runs in both, which costs eight extra tests
# across the repository and keeps the command line to a single regex.
regex="^($(cut -f2 "$plan" | LC_ALL=C sort -u | paste -sd'|'))$"

set +e
go test -tags dev -race -timeout 20m -json -run "$regex" ./... | tee "$out/events.$index.json" |
  python3 -c '
import sys, json
for line in sys.stdin:
    try:
        event = json.loads(line)
    except ValueError:
        sys.stdout.write(line)
        continue
    if event.get("Action") == "output":
        sys.stdout.write(event.get("Output", ""))
'
status=${PIPESTATUS[0]}
set -e

# A -run regex that matches nothing exits 0 with "no tests to run". That is
# exactly the shape of failure this scheme has to rule out, so what ran is
# compared against what was assigned.
python3 - "$plan" "$out/events.$index.json" <<'PY'
import json, sys

plan, events = sys.argv[1], sys.argv[2]

assigned = set()
with open(plan) as handle:
    for line in handle:
        package, _, name = line.rstrip("\n").partition("\t")
        if name:
            assigned.add((package, name))

ran = set()
with open(events) as handle:
    for line in handle:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        name = event.get("Test", "")
        if event.get("Action") == "run" and name and "/" not in name:
            ran.add((event["Package"], name))

missing = assigned - ran
if missing:
    print(f"SHARD RAN {len(assigned) - len(missing)} OF ITS {len(assigned)} TESTS", file=sys.stderr)
    for package, name in sorted(missing)[:20]:
        print(f"  did not run: {package}\t{name}", file=sys.stderr)
    sys.exit(1)

print(f"shard ran all {len(assigned)} tests it was assigned")
PY

exit "$status"
