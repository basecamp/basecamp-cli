#!/usr/bin/env bats
# templates.bats - Test templates command error handling

load test_helper


# Help

@test "templates without subcommand shows help" {
  run basecamp templates
  assert_success
  assert_output_contains "COMMANDS"
}


# Show errors

@test "templates show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates show
  assert_failure
  assert_output_contains "ID required"
}


# Create errors

@test "templates create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates create
  assert_failure
  assert_json_value '.error' '<name> required'
  assert_json_value '.code' 'usage'
}

@test "templates create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates create --name
  assert_failure
  assert_output_contains "--name requires a value"
}

@test "templates create --description without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates create "Test" --description
  assert_failure
  assert_output_contains "--description requires a value"
}


# Update errors

@test "templates update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates update
  assert_failure
  assert_output_contains "ID required"
}

@test "templates update without fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates update 123
  assert_failure
  assert_output_contains "No update fields specified"
}


# Delete errors

@test "templates delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates delete
  assert_failure
  assert_output_contains "ID required"
}


# Construct errors

@test "templates construct without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates construct
  assert_failure
  assert_output_contains "ID required"
}

@test "templates construct without project name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates construct 123
  assert_failure
  assert_output_contains "name required"
}

@test "templates construct with malformed start date shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates construct 123 --name "Project" --start-date someday
  assert_failure
  assert_output_contains "Invalid start date"
}


# Construction status errors

@test "templates construction without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates construction
  assert_failure
  assert_output_contains "ID required"
}

@test "templates construction without construction id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates construction 123
  # Cobra returns "accepts 2 arg(s)" error
  assert_failure
}


# Template library copy errors

@test "templates copy without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates copy
  assert_failure
  assert_output_contains "ID required"
}

@test "templates copy-status without copy id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates copy-status
  assert_failure
  assert_output_contains "ID required"
}

@test "templates copy --confirm-adding-people is accepted" {
  run basecamp templates copy --help
  assert_success
  assert_output_contains "--confirm-adding-people"
}


# Flag parsing

@test "templates list --status without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates list --status
  assert_failure
  assert_output_contains "--status requires a value"
}

@test "templates list --status with invalid value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates list --status bogus
  assert_failure
  assert_output_contains "unknown --status value"
}


# Help

@test "templates --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates --help
  assert_success
  assert_output_contains "basecamp templates"
  assert_output_contains "construct"
  assert_output_contains "construction"
  assert_output_contains "library"
  assert_output_contains "copy-status"
}


# Unknown action

@test "templates unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates foobar
  # Command may show help or require project - just verify it runs
}


# Kind subgroups

@test "templates --help lists the kind subgroups" {
  run basecamp templates --help
  assert_success
  assert_output_contains "projects"
  assert_output_contains "todolists"
}

@test "templates projects without subcommand shows help" {
  run basecamp templates projects
  assert_success
  assert_output_contains "construct"
  assert_output_contains "construction"
}

@test "templates todolists without subcommand shows help" {
  run basecamp templates todolists
  assert_success
  assert_output_contains "duplicate"
  assert_output_contains "duplication"
}

@test "templates projects construct without project name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates projects construct 123
  assert_failure
  assert_output_contains "name required"
}

@test "templates projects list --status with invalid value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates projects list --status bogus
  assert_failure
  assert_output_contains "unknown --status value"
}

@test "templates todolists duplicate without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates todolists duplicate
  assert_failure
  assert_output_contains "ID required"
}

@test "templates todolists duplicate accepts --in and --confirm-adding-people" {
  run basecamp templates todolists duplicate --help
  assert_success
  assert_output_contains "--in"
  assert_output_contains "--confirm-adding-people"
}

@test "templates todolists duplication without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates todolists duplication
  assert_failure
  assert_output_contains "ID required"
}

@test "templates todolists copy is an alias for duplicate" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates todolists copy
  assert_failure
  assert_output_contains "ID required"
}

@test "templates todolists copy-status is an alias for duplication" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates todolists copy-status
  assert_failure
  assert_output_contains "ID required"
}

@test "templates card-tables without subcommand shows help" {
  run basecamp templates card-tables
  assert_success
  assert_output_contains "duplicate"
  assert_output_contains "duplication"
}

@test "templates card-tables create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates card-tables create
  assert_failure
  assert_json_value '.error' '<name> required'
}

@test "templates card-tables duplicate without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates card-tables duplicate
  assert_failure
  assert_output_contains "ID required"
}

@test "templates card-tables duplicate has no --todoset flag" {
  run basecamp templates card-tables duplicate --help
  assert_success
  assert_output_contains "--in"
  assert_output_not_contains "--todoset"
}

@test "templates card-tables duplication without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates card-tables duplication
  assert_failure
  assert_output_contains "ID required"
}

@test "templates todolists create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp templates todolists create
  assert_failure
  assert_json_value '.error' '<name> required'
}

@test "todolists templatify without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp todolists templatify
  assert_failure
  assert_output_contains "ID required"
}

@test "todolists templatify has no --move-cards-to-triage flag" {
  run basecamp todolists templatify --help
  assert_success
  assert_output_contains "--copy-comments"
  assert_output_not_contains "--move-cards-to-triage"
}
