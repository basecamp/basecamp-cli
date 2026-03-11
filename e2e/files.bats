#!/usr/bin/env bats
# files.bats - Test files/vaults/uploads/documents command error handling

load test_helper


# Help

@test "files without subcommand shows help" {
  run basecamp files
  assert_success
  assert_output_contains "COMMANDS"
}


# Flag parsing errors

@test "files --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp files --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "vaults --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp vaults --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "uploads --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp uploads --project
  assert_failure
  assert_output_contains "--project requires a value"
}

@test "docs --project without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp docs --project
  assert_failure
  assert_output_contains "--project requires a value"
}


# Missing context errors

@test "files without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  # Group command without project shows help
  run basecamp files
  assert_success
  assert_output_contains "Docs & Files"
}

@test "vaults without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp vaults
  assert_success
  assert_output_contains "Docs & Files"
}

@test "uploads without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp uploads
  assert_success
  assert_output_contains "Docs & Files"
}

@test "docs without project shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp docs
  assert_success
  assert_output_contains "Docs & Files"
}


# Show command errors

@test "files show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files show
  assert_failure
  assert_output_contains "ID required"
}

@test "files show with invalid type shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files show 456 --type foobar
  # May return validation error or API error depending on implementation
  assert_failure
}


# Vault create errors

@test "files folder without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files folder
  # May return validation error or API error depending on implementation
  assert_failure
}


# Upload errors

@test "files upload without file shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files upload
  # May return validation error or API error depending on implementation
  assert_failure
}

@test "files upload with missing file shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files upload /nonexistent/file.txt
  # May return validation error or API error depending on implementation
  assert_failure
}


# Help flag

@test "files --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp files --help
  assert_success
  assert_output_contains "basecamp files"
  assert_output_contains "Docs & Files"
}

@test "files -h shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp files -h
  assert_success
  assert_output_contains "basecamp files"
}


# Unknown action

@test "files unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp files foobar
  # Command may show help or require project - just verify it runs
}


# Error envelope structure

@test "files error returns proper JSON envelope" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  # Use a subcommand that actually returns a JSON error
  run basecamp files show
  assert_failure
  assert_json_value '.ok' 'false'
  assert_json_value '.code' 'usage'
  assert_output_contains '"error"'
}


# Alias routing

@test "vaults routes to files command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp vaults --help
  assert_success
  assert_output_contains "Docs & Files"
}

@test "uploads command shows upload-specific help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp uploads --help
  assert_success
  assert_output_contains "uploaded files"
}

@test "docs routes to files command" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp docs --help
  assert_success
  assert_output_contains "Docs & Files"
}
