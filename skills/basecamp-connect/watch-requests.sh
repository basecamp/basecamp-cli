#!/usr/bin/env bash
set -u

if [ $# -ne 2 ]; then
  echo "usage: watch-requests.sh <connector-output-file> <handled-file>" >&2
  exit 2
fi

out=$1
handled=$2

if [ ! -f "$out" ]; then
  echo "watch-requests: no connector output at $out" >&2
  exit 2
fi
mkdir -p "$(dirname "$handled")"

unhandled() {
  local lines
  lines=$(($(wc -l < "$out")))
  [ "$lines" -gt 0 ] || return 1
  head -n "$lines" "$out" | awk -v handled="$handled" '
    BEGIN { while ((getline id < handled) > 0) seen[id] = 1 }
    /"type":"request"/ && match($0, /"event_id":[0-9]+/) {
      id = substr($0, RSTART + 11, RLENGTH - 11)
      if (!(id in seen)) { print; seen[id] = 1; found = 1 }
    }
    END { exit !found }'
}

until unhandled; do
  sleep 2
done
