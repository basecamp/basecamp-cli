#!/usr/bin/env bash
set -u

once=false
if [ "${1:-}" = --once ]; then
  once=true
  shift
fi
if [ $# -ne 2 ]; then
  echo "usage: watch-requests.sh [--once] <connector-output-file> <handled-file>" >&2
  exit 2
fi

out=$1
handled=$2

if [ ! -f "$out" ]; then
  echo "watch-requests: no connector output at $out" >&2
  exit 2
fi
if ! { mkdir -p "$(dirname "$handled")" && touch "$handled" && [ -f "$handled" ] && [ -r "$handled" ] && [ -w "$handled" ]; }; then
  echo "watch-requests: can't use the handled file $handled" >&2
  exit 2
fi

unhandled() {
  local lines
  if [ ! -r "$out" ]; then
    echo "watch-requests: can't read the connector output at $out" >&2
    exit 2
  fi
  lines=$(($(wc -l < "$out")))
  [ "$lines" -gt 0 ] || return 1
  head -n "$lines" "$out" | HANDLED=$handled awk '
    BEGIN { while ((getline id < ENVIRON["HANDLED"]) > 0) seen[id] = 1 }
    /"type":"request"/ && match($0, /"event_id":[0-9]+/) {
      id = substr($0, RSTART + 11, RLENGTH - 11)
      if (!(id in seen)) { print; seen[id] = 1; found = 1 }
    }
    END { exit !found }'
}

if [ "$once" = true ]; then
  unhandled
  exit 0
fi

until unhandled; do
  sleep 2
done
