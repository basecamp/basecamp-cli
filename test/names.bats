#!/usr/bin/env bats
# names.bats - Tests for lib/names.sh

load test_helper


# Test setup

setup() {
  # Call parent setup first
  _ORIG_HOME="$HOME"
  _ORIG_PWD="$PWD"

  TEST_TEMP_DIR="$(mktemp -d)"
  TEST_HOME="$TEST_TEMP_DIR/home"
  TEST_PROJECT="$TEST_TEMP_DIR/project"

  mkdir -p "$TEST_HOME/.config/basecamp"
  mkdir -p "$TEST_PROJECT/.basecamp"

  export HOME="$TEST_HOME"
  export BCQ_ROOT="${BATS_TEST_DIRNAME}/.."
  export PATH="$BCQ_ROOT/bin:$PATH"

  cd "$TEST_PROJECT"

  # Source libraries
  source "$BCQ_ROOT/lib/core.sh"
  source "$BCQ_ROOT/lib/config.sh"
  source "$BCQ_ROOT/lib/names.sh"

  # Clear cache for each test
  _names_clear_cache
}


# ============================================================================
# Project Resolution Tests
# ============================================================================

@test "resolve_project_id returns numeric ID unchanged" {
  result=$(resolve_project_id "12345")
  [[ "$result" == "12345" ]]
}

@test "resolve_project_id finds exact match" {
  # Pre-populate cache with mock data
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"},
    {"id": 222, "name": "Project Beta"},
    {"id": 333, "name": "Project Gamma"}
  ]'

  result=$(resolve_project_id "Project Beta")
  [[ "$result" == "222" ]]
}

@test "resolve_project_id finds case-insensitive match" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"},
    {"id": 222, "name": "Project Beta"}
  ]'

  result=$(resolve_project_id "project beta")
  [[ "$result" == "222" ]]
}

@test "resolve_project_id finds partial match" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"},
    {"id": 222, "name": "Project Beta"},
    {"id": 333, "name": "Another Thing"}
  ]'

  result=$(resolve_project_id "Alpha")
  [[ "$result" == "111" ]]
}

@test "resolve_project_id fails on ambiguous match" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"},
    {"id": 222, "name": "Project Beta"},
    {"id": 333, "name": "Project Gamma"}
  ]'

  # Call without command substitution to preserve RESOLVE_ERROR
  resolve_project_id "Project" >/dev/null && status=0 || status=$?
  [[ "$status" -eq 1 ]]
  [[ "$RESOLVE_ERROR" == *"Ambiguous"* ]]
}

@test "resolve_project_id shows matches in ambiguous error" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Acme Corp"},
    {"id": 222, "name": "Acme Labs"}
  ]'

  resolve_project_id "Acme" || true
  [[ "$RESOLVE_ERROR" == *"Acme Corp"* ]]
  [[ "$RESOLVE_ERROR" == *"Acme Labs"* ]]
}

@test "resolve_project_id fails on not found" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"}
  ]'

  # Don't use command substitution - call directly to preserve RESOLVE_ERROR
  resolve_project_id "Nonexistent" >/dev/null && status=0 || status=$?
  [[ "$status" -eq 1 ]]
  [[ "$RESOLVE_ERROR" == *"not found"* ]]
}


# ============================================================================
# Person Resolution Tests
# ============================================================================

@test "resolve_person_id returns numeric ID unchanged" {
  result=$(resolve_person_id "98765")
  [[ "$result" == "98765" ]]
}

@test "resolve_person_id finds exact email match" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"},
    {"id": 222, "name": "Bob Jones", "email_address": "bob@example.com"}
  ]'

  result=$(resolve_person_id "bob@example.com")
  [[ "$result" == "222" ]]
}

@test "resolve_person_id finds exact name match" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"},
    {"id": 222, "name": "Bob Jones", "email_address": "bob@example.com"}
  ]'

  result=$(resolve_person_id "Alice Smith")
  [[ "$result" == "111" ]]
}

@test "resolve_person_id finds case-insensitive name match" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"}
  ]'

  result=$(resolve_person_id "alice smith")
  [[ "$result" == "111" ]]
}

@test "resolve_person_id finds partial name match" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"},
    {"id": 222, "name": "Bob Jones", "email_address": "bob@example.com"}
  ]'

  result=$(resolve_person_id "Alice")
  [[ "$result" == "111" ]]
}

@test "resolve_person_id fails on ambiguous match" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"},
    {"id": 222, "name": "Alice Johnson", "email_address": "alicej@example.com"}
  ]'

  # Don't use command substitution - call directly to preserve RESOLVE_ERROR
  resolve_person_id "Alice" >/dev/null && status=0 || status=$?
  [[ "$status" -eq 1 ]]
  [[ "$RESOLVE_ERROR" == *"Ambiguous"* ]]
}


# ============================================================================
# Todolist Resolution Tests
# ============================================================================

@test "resolve_todolist_id returns numeric ID unchanged" {
  result=$(resolve_todolist_id "55555" "12345")
  [[ "$result" == "55555" ]]
}

@test "resolve_todolist_id requires project ID" {
  # Don't use command substitution - call directly to preserve RESOLVE_ERROR
  resolve_todolist_id "My List" "" >/dev/null && status=0 || status=$?
  [[ "$status" -eq 1 ]]
  [[ "$RESOLVE_ERROR" == *"Project ID required"* ]]
}

@test "resolve_todolist_id finds exact match" {
  # Pre-cache todoset ID to avoid API call
  _names_set_cache "todoset_12345" "99999"
  _names_set_cache "todolists_12345" '[
    {"id": 111, "name": "Sprint Tasks"},
    {"id": 222, "name": "Bug Fixes"},
    {"id": 333, "name": "Ideas"}
  ]'

  result=$(resolve_todolist_id "Bug Fixes" "12345")
  [[ "$result" == "222" ]]
}

@test "resolve_todolist_id finds case-insensitive match" {
  # Pre-cache todoset ID to avoid API call
  _names_set_cache "todoset_12345" "99999"
  _names_set_cache "todolists_12345" '[
    {"id": 111, "name": "Sprint Tasks"}
  ]'

  result=$(resolve_todolist_id "sprint tasks" "12345")
  [[ "$result" == "111" ]]
}

@test "resolve_todolist_id finds partial match" {
  # Pre-cache todoset ID to avoid API call
  _names_set_cache "todoset_12345" "99999"
  _names_set_cache "todolists_12345" '[
    {"id": 111, "name": "Sprint Tasks"},
    {"id": 222, "name": "Bug Fixes"}
  ]'

  result=$(resolve_todolist_id "Sprint" "12345")
  [[ "$result" == "111" ]]
}


# ============================================================================
# Cache Tests
# ============================================================================

@test "cache stores and retrieves data" {
  _names_set_cache "test_key" '{"foo": "bar"}'

  result=$(_names_get_cache "test_key")
  [[ "$result" == '{"foo": "bar"}' ]]
}

@test "cache returns empty for missing key" {
  result=$(_names_get_cache "nonexistent")
  [[ -z "$result" ]]
}

@test "clear_cache removes all cached data" {
  _names_set_cache "projects" '[{"id": 1}]'
  _names_set_cache "people" '[{"id": 2}]'

  _names_clear_cache

  result=$(_names_get_cache "projects")
  [[ -z "$result" ]]
}


# ============================================================================
# Suggestion Helper Tests
# ============================================================================

@test "suggest_similar finds prefix matches" {
  local data='[{"name": "Alpha"}, {"name": "Alphabetical"}, {"name": "Beta"}]'

  result=$(_names_suggest_similar "Alp" "$data" "name")
  [[ "$result" == *"Alpha"* ]]
}

@test "suggest_similar returns first few when no match" {
  local data='[{"name": "Foo"}, {"name": "Bar"}, {"name": "Baz"}]'

  result=$(_names_suggest_similar "xyz" "$data" "name")
  [[ -n "$result" ]]
}


# ============================================================================
# Error Formatting Tests
# ============================================================================

@test "format_resolve_error uses RESOLVE_ERROR if set" {
  RESOLVE_ERROR="Custom error message"

  result=$(format_resolve_error "project" "test")
  [[ "$result" == "Custom error message" ]]
}

@test "format_resolve_error uses default if RESOLVE_ERROR empty" {
  RESOLVE_ERROR=""

  result=$(format_resolve_error "project" "test-input")
  [[ "$result" == "Project not found: test-input" ]]
}


# ============================================================================
# Integration Helper Tests
# ============================================================================

@test "require_project_id resolves name to ID" {
  _names_set_cache "projects" '[
    {"id": 111, "name": "Project Alpha"},
    {"id": 222, "name": "Project Beta"}
  ]'

  result=$(require_project_id "Project Alpha")
  [[ "$result" == "111" ]]
}

@test "require_project_id passes through numeric ID" {
  result=$(require_project_id "12345")
  [[ "$result" == "12345" ]]
}

@test "require_project_id uses config when no argument" {
  export BCQ_PROJECT="99999"

  result=$(require_project_id "")
  [[ "$result" == "99999" ]]
}

@test "require_person_id resolves name to ID" {
  _names_set_cache "people" '[
    {"id": 111, "name": "Alice Smith", "email_address": "alice@example.com"}
  ]'

  result=$(require_person_id "Alice Smith")
  [[ "$result" == "111" ]]
}

@test "require_person_id resolves email to ID" {
  _names_set_cache "people" '[
    {"id": 222, "name": "Bob Jones", "email_address": "bob@example.com"}
  ]'

  result=$(require_person_id "bob@example.com")
  [[ "$result" == "222" ]]
}
