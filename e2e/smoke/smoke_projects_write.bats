#!/usr/bin/env bats
# smoke_projects_write.bats - Level 2: Project CRUD lifecycle (serial)

load smoke_helper

setup_file() {
  ensure_token || return 1
  ensure_account || return 1
}

@test "projects create creates a project" {
  run_smoke basecamp projects create "Smoke project $(date +%s)" \
    --description "Automated smoke test project" --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_not_null '.data.id'

  echo "$output" | jq -r '.data.id' > "$BATS_FILE_TMPDIR/created_project_id"
}

@test "projects update updates a project" {
  local id_file="$BATS_FILE_TMPDIR/created_project_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No project created in prior test"
  local proj_id
  proj_id=$(<"$id_file")

  run_smoke basecamp projects update "$proj_id" \
    --name "Smoke updated $(date +%s)" --json
  assert_success
  assert_json_value '.ok' 'true'
}

@test "projects wallpaper set uploads a tiled wallpaper" {
  local id_file="$BATS_FILE_TMPDIR/created_project_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No project created in prior test"
  local proj_id
  proj_id=$(<"$id_file")
  local png="$BATS_FILE_TMPDIR/wallpaper.png"
  printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==' |
    base64 -d > "$png"

  run_smoke basecamp projects wallpaper set "$png" --mode tile --project "$proj_id" --json
  if [[ "$status" -ne 0 ]] && [[ "$output" == *"not found"* || "$output" == *"404"* ]]; then
    mark_unverifiable "This Basecamp does not serve project wallpaper to API clients yet"
  fi
  assert_success
  assert_json_value '.data.wallpaper.mode' 'tile'
  assert_json_not_null '.data.wallpaper.url'
  touch "$BATS_FILE_TMPDIR/wallpaper_set"
}

@test "projects wallpaper mode changes the layout" {
  [[ -f "$BATS_FILE_TMPDIR/wallpaper_set" ]] || mark_unverifiable "No wallpaper set in prior test"
  local proj_id
  proj_id=$(<"$BATS_FILE_TMPDIR/created_project_id")

  run_smoke basecamp projects wallpaper mode fill --project "$proj_id" --json
  assert_success
  assert_json_value '.data.wallpaper.mode' 'fill'
}

@test "projects wallpaper remove clears the wallpaper" {
  [[ -f "$BATS_FILE_TMPDIR/wallpaper_set" ]] || mark_unverifiable "No wallpaper set in prior test"
  local proj_id
  proj_id=$(<"$BATS_FILE_TMPDIR/created_project_id")

  run_smoke basecamp projects wallpaper remove --project "$proj_id" --json
  assert_success
  assert_json_value '.data.wallpaper' 'null'
}

@test "projects delete deletes a project" {
  local id_file="$BATS_FILE_TMPDIR/created_project_id"
  [[ -f "$id_file" ]] || mark_unverifiable "No project created in prior test"
  local proj_id
  proj_id=$(<"$id_file")

  run_smoke basecamp projects delete "$proj_id" --json
  assert_success
  assert_json_value '.ok' 'true'
}
