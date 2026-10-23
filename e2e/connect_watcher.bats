#!/usr/bin/env bats

# Focused regressions for the shipped request-watching script, using the
# existing BATS suite. No Basecamp server or credentials are used.
setup() {
  WATCHER="${WATCHER_UNDER_TEST:-$BATS_TEST_DIRNAME/../skills/basecamp-connect/watch-requests.sh}"
  WORK="$(mktemp -d)"
  OUT="$WORK/connector.log"
  HANDLED="$WORK/handled"
  : > "$OUT"
}

teardown() {
  if [ -n "${WATCH_PID:-}" ]; then
    kill "$WATCH_PID" 2>/dev/null || true
    wait "$WATCH_PID" 2>/dev/null || true
  fi
  chmod -R u+rwX "$WORK"
  rm -rf "$WORK"
}

@test "watcher reports usage without its required paths" {
  run bash "$WATCHER"
  [ "$status" -eq 2 ]
  [[ "$output" == *usage:* ]]
}

@test "watcher refuses a missing connector output" {
  run bash "$WATCHER" --once "$WORK/missing" "$HANDLED"
  [ "$status" -eq 2 ]
  [[ "$output" == *"no connector output"* ]]
}

@test "watcher can drain an empty output" {
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "watcher creates the handled directory and returns a fresh request" {
  printf '%s\n' '{"type":"request","event_id":101}' > "$OUT"
  run bash "$WATCHER" --once "$OUT" "$WORK/nested/handled"
  [ "$status" -eq 0 ]
  [ "$output" = '{"type":"request","event_id":101}' ]
  [ -f "$WORK/nested/handled" ]
}

@test "watcher skips ids already handled" {
  printf '%s\n' '{"type":"request","event_id":101}' > "$OUT"
  printf '%s\n' 101 > "$HANDLED"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "watcher matches handled ids exactly rather than by prefix" {
  printf '%s\n' '{"type":"request","event_id":101}' '{"type":"request","event_id":1011}' > "$OUT"
  printf '%s\n' 101 > "$HANDLED"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ "$output" = '{"type":"request","event_id":1011}' ]
}

@test "watcher emits a duplicated request once" {
  printf '%s\n' '{"type":"request","event_id":101}' '{"type":"request","event_id":101}' > "$OUT"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ "$output" = '{"type":"request","event_id":101}' ]
}

@test "watcher ignores event and pointer lines" {
  printf '%s\n' '{"type":"event","event_id":101}' '{"event_id":102}' '{"type":"request","event_id":103}' > "$OUT"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ "$output" = '{"type":"request","event_id":103}' ]
}

@test "watcher does not deliver a partial line until it is complete" {
  printf '%s\n' '{"type":"event","event_id":100}' > "$OUT"
  printf '%s' '{"type":"request","event_id":101}' >> "$OUT"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  printf '\n' >> "$OUT"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [ "$output" = '{"type":"request","event_id":101}' ]
}

@test "watcher does not mistake an escaped id in content for the request id" {
  printf '%s\n' '{"type":"request","content":"quoted \"event_id\":999","event_id":101}' > "$OUT"
  printf '%s\n' 999 > "$HANDLED"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"event_id":101}' ]]
}

@test "watcher preserves backslashes in the handled path" {
  printf '%s\n' '{"type":"request","event_id":101}' > "$OUT"
  printf '%s\n' 101 > "$WORK/handled\name"
  run bash "$WATCHER" --once "$OUT" "$WORK/handled\name"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "watcher refuses an unusable handled path" {
  mkdir "$HANDLED"
  run bash "$WATCHER" --once "$OUT" "$HANDLED"
  [ "$status" -eq 2 ]
  [[ "$output" == *"can't use the handled file"* ]]
}

@test "watcher wakes when a new complete request arrives" {
  bash "$WATCHER" "$OUT" "$HANDLED" > "$WORK/result" 2> "$WORK/errors" &
  WATCH_PID=$!
  printf '%s\n' '{"type":"request","event_id":101}' >> "$OUT"
  for _ in {1..100}; do
    [ ! -s "$WORK/result" ] || break
    sleep 0.05
  done
  [ -s "$WORK/result" ]
  for _ in {1..100}; do
    kill -0 "$WATCH_PID" 2>/dev/null || break
    sleep 0.05
  done
  ! kill -0 "$WATCH_PID" 2>/dev/null
  wait "$WATCH_PID"
  WATCH_PID=
  [ "$(< "$WORK/result")" = '{"type":"request","event_id":101}' ]
}
