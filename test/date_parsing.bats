#!/usr/bin/env bats
# date_parsing.bats - Tests for natural date parsing

load test_helper


setup() {
  # Call parent setup
  _ORIG_HOME="$HOME"
  _ORIG_PWD="$PWD"
  TEST_TEMP_DIR="$(mktemp -d)"
  export BCQ_ROOT="${BATS_TEST_DIRNAME}/.."
}

teardown() {
  export HOME="$_ORIG_HOME"
  cd "$_ORIG_PWD" 2>/dev/null || true
  [[ -d "$TEST_TEMP_DIR" ]] && rm -rf "$TEST_TEMP_DIR"
}


# Basic date keywords

@test "parse_date handles 'today'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "today")
  expected=$(date +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles 'tomorrow'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "tomorrow")
  expected=$(date -v+1d +%Y-%m-%d 2>/dev/null || date -d "+1 day" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles 'yesterday'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "yesterday")
  expected=$(date -v-1d +%Y-%m-%d 2>/dev/null || date -d "-1 day" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}


# Relative dates

@test "parse_date handles 'next week'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "next week")
  expected=$(date -v+7d +%Y-%m-%d 2>/dev/null || date -d "+7 days" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles 'next month'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "next month")
  expected=$(date -v+1m +%Y-%m-%d 2>/dev/null || date -d "+1 month" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles '+N' format" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "+5")
  expected=$(date -v+5d +%Y-%m-%d 2>/dev/null || date -d "+5 days" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles 'in N days'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "in 3 days")
  expected=$(date -v+3d +%Y-%m-%d 2>/dev/null || date -d "+3 days" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}

@test "parse_date handles 'in N weeks'" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "in 2 weeks")
  expected=$(date -v+14d +%Y-%m-%d 2>/dev/null || date -d "+14 days" +%Y-%m-%d)
  [[ "$result" == "$expected" ]]
}


# Weekday names

@test "parse_date handles weekday names" {
  source "$BCQ_ROOT/lib/core.sh"

  # Just verify it returns a valid date format
  result=$(parse_date "monday")
  [[ "$result" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]
}

@test "parse_date handles short weekday names" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "fri")
  [[ "$result" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]
}

@test "parse_date handles 'next' weekday" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "next monday")
  [[ "$result" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]

  # Should be at least 7 days from now
  today=$(date +%Y-%m-%d)
  [[ "$result" > "$today" ]]
}


# End of period

@test "parse_date handles 'eow' (end of week)" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "eow")
  [[ "$result" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]
}

@test "parse_date handles 'eom' (end of month)" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "eom")
  [[ "$result" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]

  # Should be 28-31 depending on month
  day=$(echo "$result" | cut -d- -f3)
  [[ "$day" -ge 28 ]]
}


# Pass-through formats

@test "parse_date passes through YYYY-MM-DD format" {
  source "$BCQ_ROOT/lib/core.sh"

  result=$(parse_date "2024-12-25")
  [[ "$result" == "2024-12-25" ]]
}


# Case insensitivity

@test "parse_date is case insensitive" {
  source "$BCQ_ROOT/lib/core.sh"

  result_lower=$(parse_date "tomorrow")
  result_upper=$(parse_date "TOMORROW")
  result_mixed=$(parse_date "ToMoRrOw")

  [[ "$result_lower" == "$result_upper" ]]
  [[ "$result_lower" == "$result_mixed" ]]
}
