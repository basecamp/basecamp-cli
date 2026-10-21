#!/usr/bin/env bats
# smoke_embeds.bats - Level 1: Embeds for rich text

load smoke_helper

setup_file() {
  ensure_token || return 1
}

@test "embeds without subcommand shows help" {
  run basecamp embeds
  assert_success
  assert_output_contains "create"
}

@test "embeds create returns an embed with its sgid" {
  run_smoke basecamp embeds create "https://www.youtube.com/watch?v=dQw4w9WgXcQ" --json
  if [[ "$status" -ne 0 ]] && [[ "$output" == *"not found"* || "$output" == *"404"* ]]; then
    mark_unverifiable "This Basecamp does not serve POST /embeds.json to API clients yet"
    return
  fi
  assert_success
  assert_json_not_null '.data.sgid'
  echo "$output" | jq -r '.data.sgid' > "$BATS_FILE_TMPDIR/embed_sgid"
}

@test "embeds show returns the embed" {
  [[ -s "$BATS_FILE_TMPDIR/embed_sgid" ]] || mark_unverifiable "No embed was created to show"
  run_smoke basecamp embeds show "$(cat "$BATS_FILE_TMPDIR/embed_sgid")" --json
  assert_success
  assert_json_value '.data.url' 'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
}
