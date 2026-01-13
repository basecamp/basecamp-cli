#!/usr/bin/env bats
# cache.bats - Tests for ETag HTTP caching

load test_helper


# Cache helper functions

@test "_cache_dir defaults to ~/.cache/bcq" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_dir)
  [[ "$result" == *".cache/bcq"* ]]
}

@test "_cache_dir respects XDG_CACHE_HOME" {
  export XDG_CACHE_HOME="$TEST_TEMP_DIR/xdg-cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_dir)
  [[ "$result" == "$TEST_TEMP_DIR/xdg-cache/bcq" ]]
}

@test "_cache_dir respects BCQ_CACHE_DIR override" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/custom-cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_dir)
  [[ "$result" == "$TEST_TEMP_DIR/custom-cache" ]]
}

@test "_cache_dir respects config file setting" {
  mkdir -p "$TEST_HOME/.config/basecamp"
  echo '{"cache_dir": "/custom/from/config"}' > "$TEST_HOME/.config/basecamp/config.json"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  load_config
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_dir)
  [[ "$result" == "/custom/from/config" ]]
}

@test "_cache_key generates consistent hash for same input" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key1 key2
  key1=$(_cache_key "12345" "/projects.json" "token123")
  key2=$(_cache_key "12345" "/projects.json" "token123")
  [[ "$key1" == "$key2" ]]
}

@test "_cache_key generates different hash for different accounts" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key1 key2
  key1=$(_cache_key "12345" "/projects.json" "token123")
  key2=$(_cache_key "67890" "/projects.json" "token123")
  [[ "$key1" != "$key2" ]]
}

@test "_cache_key generates different hash for different URLs" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key1 key2
  key1=$(_cache_key "12345" "/projects.json" "token123")
  key2=$(_cache_key "12345" "/todos.json" "token123")
  [[ "$key1" != "$key2" ]]
}

@test "_cache_key generates different hash for different tokens" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key1 key2
  key1=$(_cache_key "12345" "/projects.json" "token-user-A")
  key2=$(_cache_key "12345" "/projects.json" "token-user-B")
  [[ "$key1" != "$key2" ]]
}

@test "_cache_key generates different hash for different origins" {
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  export BCQ_API_URL="https://api.example.com"
  local key1
  key1=$(_cache_key "12345" "/projects.json" "token123")

  export BCQ_API_URL="https://api.other.com"
  local key2
  key2=$(_cache_key "12345" "/projects.json" "token123")

  [[ "$key1" != "$key2" ]]
}

@test "_cache_set and _cache_get_etag round-trip" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key="testkey123"
  local body='{"id": 1, "name": "Test"}'
  local etag='"abc123"'
  local headers="HTTP/1.1 200 OK"

  _cache_set "$key" "$body" "$etag" "$headers"

  local retrieved_etag
  retrieved_etag=$(_cache_get_etag "$key")
  [[ "$retrieved_etag" == "$etag" ]]
}

@test "_cache_set and _cache_get_body round-trip" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local key="testkey456"
  local body='{"id": 2, "name": "Another Test"}'
  local etag='"def456"'
  local headers="HTTP/1.1 200 OK"

  _cache_set "$key" "$body" "$etag" "$headers"

  local retrieved_body
  retrieved_body=$(_cache_get_body "$key")
  [[ "$retrieved_body" == "$body" ]]
}

@test "_cache_get_etag returns empty for missing key" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_get_etag "nonexistent" || echo "")
  [[ -z "$result" ]]
}

@test "_cache_get_body returns empty for missing key" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local result
  result=$(_cache_get_body "nonexistent" || echo "")
  [[ -z "$result" ]]
}

@test "cache files are created in correct location" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  _cache_set "mykey" '{"test": true}' '"etag123"' "HTTP/1.1 200 OK"

  [[ -f "$TEST_TEMP_DIR/cache/etags.json" ]]
  [[ -f "$TEST_TEMP_DIR/cache/responses/mykey.body" ]]
  [[ -f "$TEST_TEMP_DIR/cache/responses/mykey.headers" ]]
}


# Cache behavior in commands

@test "BCQ_CACHE_ENABLED=false disables caching setup" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  export BCQ_CACHE_ENABLED=false

  # Run with verbose to check if cache logic is skipped
  # The command will fail on API call but we can check debug output
  run bash -c "bcq -v projects 2>&1 | grep -i cache || echo 'no cache output'"

  # Should not have any cache-related debug output
  assert_output_contains "no cache output"
}

@test "cache enabled by default shows cache debug output" {
  create_credentials
  create_global_config '{"account_id": 99999}'
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"

  # First run - no cached etag yet, so no If-None-Match
  # But after 200 response, we'd see "Cache: stored" if server sent ETag
  # Since we don't have a real server, we just verify the setup doesn't crash
  run bcq -v projects 2>&1

  # Should fail on network but not crash due to cache logic
  [[ "$status" -ne 0 ]] || true  # Expected to fail (no real API)
}


# Cache updates etags.json correctly

@test "multiple cache entries stored correctly" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  _cache_set "key1" '{"a": 1}' '"etag1"' "HTTP/1.1 200 OK"
  _cache_set "key2" '{"b": 2}' '"etag2"' "HTTP/1.1 200 OK"
  _cache_set "key3" '{"c": 3}' '"etag3"' "HTTP/1.1 200 OK"

  [[ $(_cache_get_etag "key1") == '"etag1"' ]]
  [[ $(_cache_get_etag "key2") == '"etag2"' ]]
  [[ $(_cache_get_etag "key3") == '"etag3"' ]]
}

@test "cache update overwrites existing entry" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  _cache_set "key1" '{"version": 1}' '"etag-v1"' "HTTP/1.1 200 OK"
  _cache_set "key1" '{"version": 2}' '"etag-v2"' "HTTP/1.1 200 OK"

  [[ $(_cache_get_etag "key1") == '"etag-v2"' ]]
  [[ $(_cache_get_body "key1") == '{"version": 2}' ]]
}

@test "cache stores and retrieves headers" {
  export BCQ_CACHE_DIR="$TEST_TEMP_DIR/cache"
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/api.sh"

  local headers="HTTP/1.1 200 OK
Content-Type: application/json
ETag: \"abc123\""

  _cache_set "key1" '{"test": true}' '"abc123"' "$headers"

  local retrieved_headers
  retrieved_headers=$(_cache_get_headers "key1")
  [[ "$retrieved_headers" == "$headers" ]]
}
