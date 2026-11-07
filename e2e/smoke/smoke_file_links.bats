#!/usr/bin/env bats
# smoke_file_links.bats - Level 1: Cloud files and Google documents in Docs & Files

load smoke_helper

setup_file() {
  # Each update reads the id its create wrote.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true
  ensure_token || return 1
  ensure_project || return 1
}

@test "files cloud-files create adds a cloud file" {
  run_smoke basecamp files cloud-files create "Smoke cloud file" \
    --url "https://example.com/smoke-$(date +%s)" --description "Kept" \
    --no-subscribe -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.service.code' 'other'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/cloud_file_id"
}

@test "files cloud-files update renames a cloud file and keeps the rest" {
  local id_file="$BATS_FILE_TMPDIR/cloud_file_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No cloud file created in prior test"
  local cloud_file_id
  cloud_file_id=$(<"$id_file")

  run_smoke basecamp files cloud-files update "$cloud_file_id" --title "Smoke cloud file renamed" --json
  assert_success
  assert_json_value '.data.title' 'Smoke cloud file renamed'
  assert_json_value '.data.service.code' 'other'
  assert_json_value '.data.description | contains("Kept")' 'true'
}

@test "files google-docs create adds a Google document" {
  run_smoke basecamp files google-docs create "Smoke Google doc" \
    --url "https://docs.google.com/document/d/smoke-$(date +%s)/edit" \
    --draft --no-subscribe -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.document_type' 'doc'
  assert_json_value '.data.status' 'drafted'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/google_doc_id"
}

@test "files google-docs update renames a Google document and keeps it a draft" {
  local id_file="$BATS_FILE_TMPDIR/google_doc_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No Google document created in prior test"
  local google_doc_id
  google_doc_id=$(<"$id_file")

  run_smoke basecamp files google-docs update "$google_doc_id" --title "Smoke Google doc renamed" --json
  assert_success
  assert_json_value '.data.title' 'Smoke Google doc renamed'
  assert_json_value '.data.document_type' 'doc'
  assert_json_value '.data.status' 'drafted'
}
