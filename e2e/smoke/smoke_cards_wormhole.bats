#!/usr/bin/env bats
# smoke_cards_wormhole.bats - Level 1: Card-table wormholes (cross-project move).
#
# `list` is a read-only exercise. create/update/delete are always-unverifiable:
# a wormhole's destination must be a column on ANOTHER accessible card table, and
# the smoke environment provisions only one card table — there is no CLI command
# to create a second one to point at. The command wiring is covered by unit tests
# in internal/commands/cards_test.go.

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
  ensure_cardtable || return 1
}

@test "cards wormholes list returns the card table's wormholes" {
  run_smoke basecamp cards wormholes list \
    --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "cards wormholes create needs a cross-project destination table" {
  mark_unverifiable "Requires a column on another accessible card table; smoke provisions only one and there is no CLI to create a second"
}

@test "cards wormholes update needs an existing wormhole and destination table" {
  mark_unverifiable "Requires an existing wormhole plus a second accessible destination card table (see create)"
}

@test "cards wormholes delete needs an existing wormhole" {
  mark_unverifiable "Requires an existing wormhole, which create cannot provision in the smoke environment"
}

@test "cards move --to-wormhole needs a linked wormhole and is destructive" {
  mark_unverifiable "Requires a linked wormhole (see create) and teleporting deletes the source card asynchronously — not safe to exercise against QA data"
}
