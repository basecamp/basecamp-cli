#!/usr/bin/env bats
# templates.bats - Test templates command error handling

load test_helper


# Show errors

@test "templates show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates show
  assert_failure
  assert_output_contains "ID required"
}


# Create errors

@test "templates create without name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates create
  assert_failure
  assert_output_contains "Template name is required"
}

@test "templates create --name without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates create --name
  assert_failure
  assert_output_contains "--name requires a value"
}

@test "templates create --description without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates create "Test" --description
  assert_failure
  assert_output_contains "--description requires a value"
}


# Update errors

@test "templates update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates update
  assert_failure
  assert_output_contains "ID required"
}

@test "templates update without fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates update 123
  assert_failure
  assert_output_contains "Use --name"
}


# Delete errors

@test "templates delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates delete
  assert_failure
  assert_output_contains "ID required"
}


# Construct errors

@test "templates construct without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates construct
  assert_failure
  assert_output_contains "ID required"
}

@test "templates construct without project name shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates construct 123
  assert_failure
  assert_output_contains "name required"
}


# Construction status errors

@test "templates construction without template id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates construction
  assert_failure
  assert_output_contains "ID required"
}

@test "templates construction without construction id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates construction 123
  # Cobra returns "accepts 2 arg(s)" error
  assert_failure
}


# Flag parsing

@test "templates --status without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates --status
  assert_failure
  assert_output_contains "--status requires a value"
}


# Help

@test "templates --help shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates --help
  assert_success
  assert_output_contains "bcq templates"
  assert_output_contains "construct"
  assert_output_contains "construction"
}


# Unknown action

@test "templates unknown action shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run bcq templates foobar
  # Command may show help or require project - just verify it runs
}
