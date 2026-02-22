#!/usr/bin/env bats
# cards_columns_steps.bats - Test card, column, and step command error handling

load test_helper


# Card create tests

@test "card --help shows help with --content option" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp card --help
  assert_success
  assert_output_contains "basecamp card"
  assert_output_contains "--content"
  assert_output_contains "--title"
}

@test "card --content without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp card --title "Test" --content
  assert_failure
  assert_output_contains "--content requires a value"
}

@test "card with unknown option shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp card --title "Test" --foo
  assert_failure
  assert_output_contains "Unknown option: --foo"
}

@test "card without title shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp card --content "Body only"
  assert_failure
  assert_output_contains "title required"
}


# Column show errors

@test "cards column show without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column show
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column show without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp cards column show 456
  assert_failure
  assert_output_contains "project"
}


# Column create errors

@test "cards column create without title shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column create
  assert_failure
  assert_output_contains "title required"
}


# Column update errors

@test "cards column update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column update
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column update without fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column update 456
  assert_failure
  assert_output_contains "update"
}


# Column move errors

@test "cards column move without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column move
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column move without position shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column move 456
  assert_failure
  assert_output_contains "--position required"
}


# Column watch/unwatch errors

@test "cards column watch without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column watch
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column unwatch without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column unwatch
  assert_failure
  assert_output_contains "ID required"
}


# Column on-hold errors

@test "cards column on-hold without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column on-hold
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column no-on-hold without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column no-on-hold
  assert_failure
  assert_output_contains "ID required"
}


# Column color errors

@test "cards column color without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column color
  assert_failure
  assert_output_contains "ID required"
}

@test "cards column color without color value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column color 456
  assert_failure
  assert_output_contains "--color is required"
}


# Column unknown action - Cobra shows help for unknown subcommands

@test "cards column unknown action shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp cards column foobar
  # Cobra doesn't error on unknown args, shows help
  assert_success
  assert_output_contains "Available Commands"
}


# Cards columns --card-table option

@test "cards columns --card-table without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards columns --card-table
  assert_failure
  assert_output_contains "--card-table requires a value"
}

@test "cards column create --card-table without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column create "Test" --card-table
  assert_failure
  assert_output_contains "--card-table requires a value"
}

@test "cards column move --card-table without value shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards column move 456 --position 1 --card-table
  assert_failure
  assert_output_contains "--card-table requires a value"
}


# Steps list errors

@test "cards steps without card id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards steps
  assert_failure
  assert_output_contains "ID required"
}

@test "cards steps without project shows error" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp cards steps 456
  assert_failure
  assert_output_contains "project"
}


# Step create errors

@test "cards step create without title shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step create --card 456
  assert_failure
  assert_output_contains "title required"
}

@test "cards step create without card shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step create "My step"
  assert_failure
  # Cobra shows required flag(s) not set message
  assert_output_contains "card"
}


# Step update errors

@test "cards step update without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step update
  assert_failure
  assert_output_contains "ID required"
}

@test "cards step update without fields shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step update 456
  assert_failure
  assert_output_contains "update"
}


# Step complete/uncomplete errors

@test "cards step complete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step complete
  assert_failure
  assert_output_contains "ID required"
}

@test "cards step uncomplete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step uncomplete
  assert_failure
  assert_output_contains "ID required"
}


# Step move errors

@test "cards step move without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step move
  assert_failure
  assert_output_contains "ID required"
}

@test "cards step move without card shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step move 456 --position 1
  assert_failure
  assert_output_contains "--card is required"
}

@test "cards step move without position shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step move 456 --card 789
  assert_failure
  assert_output_contains "--position is required"
}


# Step delete errors

@test "cards step delete without id shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp cards step delete
  assert_failure
  assert_output_contains "ID required"
}


# Step unknown action - Cobra shows help for unknown subcommands

@test "cards step unknown action shows help" {
  create_credentials
  create_global_config '{"account_id": 99999}'

  run basecamp cards step foobar
  # Cobra doesn't error on unknown args, shows help
  assert_success
  assert_output_contains "Available Commands"
}
