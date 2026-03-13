#!/usr/bin/env bats
# smoke_files_read.bats - Level 0: Files, vaults, docs, uploads (read-only)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "files list returns files" {
  run_smoke basecamp files list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "vaults list returns vaults" {
  run_smoke basecamp vaults list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "docs list returns documents" {
  run_smoke basecamp docs list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "uploads list returns uploads" {
  run_smoke basecamp uploads list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "vaults show returns vault detail" {
  ensure_vault || mark_unverifiable "No vault in project"
  run_smoke basecamp vaults show "$QA_VAULT" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "docs show returns document detail" {
  local out
  out=$(basecamp docs list -p "$QA_PROJECT" --json 2>/dev/null) || mark_unverifiable "Cannot list docs"
  local doc_id
  doc_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$doc_id" ]] || mark_unverifiable "No documents in project"

  run_smoke basecamp docs show "$doc_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "files folders list returns folders" {
  run_smoke basecamp files folders list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files documents list returns documents" {
  run_smoke basecamp files documents list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files uploads list returns uploads" {
  run_smoke basecamp files uploads list -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files download downloads a file" {
  ensure_upload || return 0

  run_smoke basecamp files download "$QA_UPLOAD" -p "$QA_PROJECT" -o "$BATS_FILE_TMPDIR/smoke_download"
  assert_success
}

@test "docs download downloads a document" {
  local out
  out=$(basecamp docs list -p "$QA_PROJECT" --json 2>/dev/null) || mark_unverifiable "Cannot list docs"
  local doc_id
  doc_id=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$doc_id" ]] || mark_unverifiable "No documents in project"

  run_smoke basecamp docs download "$doc_id" -p "$QA_PROJECT" -o "$BATS_FILE_TMPDIR/smoke_doc_download"
  assert_success
}
