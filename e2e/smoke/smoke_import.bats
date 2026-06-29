#!/usr/bin/env bats
# smoke_import.bats - Deterministic import artifact commands

load smoke_helper

write_smoke_import_files() {
  cat > tasks.csv <<'CSV'
id,title,list
1,First,Backlog
2,Second,Doing
CSV
  cat > mapping.json <<'JSON'
{
  "schema_version": 1,
  "record_id": { "column_index": 0 },
  "title": { "column_index": 1 },
  "todolist": { "column_index": 2 }
}
JSON
  cat > destination.json <<'JSON'
{
  "schema_version": 1,
  "mode": "existing_project",
  "project_id": "12345",
  "todolist_strategy": "create_from_column"
}
JSON
}

inspect_smoke_import_csv() {
  run_smoke basecamp import inspect tasks.csv --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.status' 'profiled'
  printf '%s\n' "$output" > inspection.json
}

compile_smoke_import_artifact() {
  inspect_smoke_import_csv
  run_smoke basecamp import compile --inspection inspection.json --mapping mapping.json --destination destination.json --out basecamp-import --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.status' 'compiled'
}

@test "import inspect profiles local CSV" {
  write_smoke_import_files
  run_smoke basecamp import inspect tasks.csv --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.status' 'profiled'
}

@test "import compile creates local artifact" {
  write_smoke_import_files
  compile_smoke_import_artifact
}

@test "import plan reads local artifact" {
  write_smoke_import_files
  compile_smoke_import_artifact

  run_smoke basecamp import plan --artifact basecamp-import --json
  assert_success
  assert_json_value '.ok' 'true'
  assert_json_value '.data.status' 'ready_for_approval'
}

@test "import status requires artifact" {
  run_smoke basecamp import status --json
  assert_failure
  assert_output_contains "--artifact required"
}

@test "import repair requires artifact" {
  run_smoke basecamp import repair --json
  assert_failure
  assert_output_contains "--artifact required"
}

@test "import followup requires artifact" {
  run_smoke basecamp import followup --json
  assert_failure
  assert_output_contains "--artifact required"
}

@test "import preflight requires artifact" {
  run_smoke basecamp import preflight --json
  assert_failure
  assert_output_contains "--artifact required"
}

@test "import execute requires approval" {
  write_smoke_import_files
  compile_smoke_import_artifact

  run_smoke basecamp import execute --artifact basecamp-import --json
  assert_failure
  assert_output_contains "--approved required"
}
