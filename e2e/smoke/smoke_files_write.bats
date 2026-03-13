#!/usr/bin/env bats
# smoke_files_write.bats - Level 1: File upload and folder operations

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_project || return 1
}

@test "files folders create creates a vault" {
  run_smoke basecamp files folders create "Smoke vault $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "attach uploads a file" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_attach.txt"
  echo "smoke test content $(date +%s)" > "$tmpfile"

  run_smoke basecamp attach "$tmpfile" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "upload shortcut uploads a file" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_upload.txt"
  echo "smoke upload content $(date +%s)" > "$tmpfile"

  run_smoke basecamp upload "$tmpfile" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/upload_id"
}

@test "uploads create creates an upload" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_uploads_create.txt"
  echo "smoke uploads create content $(date +%s)" > "$tmpfile"

  run_smoke basecamp uploads create "$tmpfile" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/uploads_create_id"
}

@test "uploads show returns upload detail" {
  local id_file="$BATS_FILE_TMPDIR/uploads_create_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local upload_id
  upload_id=$(<"$id_file")

  run_smoke basecamp uploads show "$upload_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "files show returns file detail" {
  local id_file="$BATS_FILE_TMPDIR/upload_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local file_id
  file_id=$(<"$id_file")

  run_smoke basecamp files show "$file_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "docs documents create creates a document" {
  run_smoke basecamp docs documents create "Smoke doc $(date +%s)" \
    "Automated smoke test document" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "files documents create creates a document" {
  run_smoke basecamp files documents create "Smoke file doc $(date +%s)" \
    "Automated smoke test document" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "files uploads create creates an upload" {
  local tmpfile="$BATS_FILE_TMPDIR/smoke_files_upload.txt"
  echo "files upload content $(date +%s)" > "$tmpfile"

  run_smoke basecamp files uploads create "$tmpfile" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}

@test "files update updates a file" {
  local id_file="$BATS_FILE_TMPDIR/upload_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local file_id
  file_id=$(<"$id_file")

  run_smoke basecamp files update "$file_id" \
    --description "Updated smoke file $(date +%s)" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files archive archives a file" {
  local id_file="$BATS_FILE_TMPDIR/upload_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local file_id
  file_id=$(<"$id_file")

  run_smoke basecamp files archive "$file_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files restore restores an archived file" {
  local id_file="$BATS_FILE_TMPDIR/upload_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local file_id
  file_id=$(<"$id_file")

  run_smoke basecamp files restore "$file_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "files trash trashes a file" {
  local id_file="$BATS_FILE_TMPDIR/upload_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No upload created in prior test"
  local file_id
  file_id=$(<"$id_file")

  run_smoke basecamp files trash "$file_id" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "vaults folders create creates a vault folder" {
  ensure_vault || return 0

  run_smoke basecamp vaults folders create "Smoke vault folder $(date +%s)" \
    --vault "$QA_VAULT" -p "$QA_PROJECT" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'
}
