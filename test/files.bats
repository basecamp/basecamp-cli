#!/usr/bin/env bats
# files.bats - Test files/vaults/uploads/documents command error handling

load test_helper


# Flag parsing errors

@test "files --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq files --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "vaults --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq vaults --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "uploads --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq uploads --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "docs --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq docs --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Missing context errors

@test "files without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq files
  assert_failure
  assert_output_contains "No project specified"
}

@test "vaults without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq vaults
  assert_failure
  assert_output_contains "No project specified"
}

@test "uploads without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq uploads
  assert_failure
  assert_output_contains "No project specified"
}

@test "docs without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq docs
  assert_failure
  assert_output_contains "No project specified"
}


# Show command errors

@test "files show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files show
  assert_failure
  assert_output_contains "Item ID required"
}

@test "files show with invalid type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files show 456 --type foobar
  assert_failure
  assert_output_contains "Invalid type: foobar"
}


# Vault create errors

@test "files folder without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files folder
  assert_failure
  assert_output_contains "Folder name required"
}


# Upload errors

@test "files upload without file shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files upload
  assert_failure
  assert_output_contains "File path required"
}

@test "files upload with missing file shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files upload /nonexistent/file.txt
  assert_failure
  assert_output_contains "File not found"
}


# Help flag

@test "files --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq files --help
  assert_success
  assert_output_contains "bcq files"
  assert_output_contains "Docs & Files"
}

@test "files -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq files -h
  assert_success
  assert_output_contains "bcq files"
}


# Unknown action

@test "files unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run bcq files foobar
  assert_failure
  assert_output_contains "Unknown files action"
}


# Error envelope structure

@test "files error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq files
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}


# Alias routing

@test "vaults routes to files command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq vaults --help
  assert_success
  assert_output_contains "Docs & Files"
}

@test "uploads routes to files command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq uploads --help
  assert_success
  assert_output_contains "Docs & Files"
}

@test "docs routes to files command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq docs --help
  assert_success
  assert_output_contains "Docs & Files"
}
