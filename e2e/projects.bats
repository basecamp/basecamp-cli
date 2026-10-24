#!/usr/bin/env bats
# projects.bats - Test projects command input handling

load test_helper


# Help

@test "projects wallpaper without subcommand shows help" {
  run basecamp projects wallpaper
  assert_success
  assert_output_contains "COMMANDS"
  assert_output_contains "set"
  assert_output_contains "mode"
  assert_output_contains "remove"
}

@test "projects wallpaper set help names the formats and the size cap" {
  run basecamp projects wallpaper set --help
  assert_success
  assert_output_contains "PNG, JPEG, GIF, WebP, AVIF, HEIF, or HEIC files up to 10 MB"
}


# Input errors that need no network

@test "projects wallpaper set without a file shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp projects wallpaper set
  assert_failure
  assert_json_value '.error' '<file> required'
  assert_json_value '.code' 'usage'
}

@test "projects wallpaper set refuses an SVG" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'
  printf '<svg></svg>' > image.svg

  run basecamp projects wallpaper set image.svg
  assert_failure
  assert_json_value '.error' 'image.svg: unsupported image type image/svg+xml (use PNG, JPEG, GIF, WebP, AVIF, HEIF, or HEIC)'
  assert_json_value '.code' 'usage'
}

@test "projects wallpaper set refuses a file over 10 MB" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'
  dd if=/dev/zero of=big.png bs=1 count=1 seek=10485760 2>/dev/null

  run basecamp projects wallpaper set big.png
  assert_failure
  assert_json_value '.error' 'big.png exceeds maximum wallpaper size of 10 MB'
}

@test "projects wallpaper set refuses an unknown --mode" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'
  printf '\x89PNG\r\n\x1a\n' > skyline.png

  run basecamp projects wallpaper set skyline.png --mode stretch
  assert_failure
  assert_json_value '.error' 'unknown wallpaper mode "stretch"'
  assert_json_value '.hint' 'Use fill or tile'
}

@test "projects wallpaper mode without a mode shows error" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp projects wallpaper mode
  assert_failure
  assert_json_value '.error' '<fill|tile> required'
}

@test "projects wallpaper mode refuses an unknown mode" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp projects wallpaper mode stretch
  assert_failure
  assert_json_value '.error' 'unknown wallpaper mode "stretch"'
}

@test "projects wallpaper remove takes no arguments" {
  create_credentials
  create_global_config '{"account_id": 99999, "project_id": 123}'

  run basecamp projects wallpaper remove skyline.png
  assert_failure
}
