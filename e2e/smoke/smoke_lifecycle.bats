#!/usr/bin/env bats
# smoke_lifecycle.bats - Level 3: Lifecycle and infra commands
# These require ephemeral accounts, interactive prompts, or are infra-only.

load smoke_helper

@test "auth login is out of scope" {
  mark_out_of_scope "Interactive OAuth flow"
}

@test "auth logout is out of scope" {
  mark_out_of_scope "Interactive OAuth flow"
}

@test "auth refresh is out of scope" {
  mark_out_of_scope "Requires OAuth credentials"
}

@test "login is out of scope" {
  mark_out_of_scope "Alias for auth login — interactive OAuth flow"
}

@test "logout is out of scope" {
  mark_out_of_scope "Alias for auth logout — interactive OAuth flow"
}

@test "setup is out of scope" {
  mark_out_of_scope "Interactive onboarding wizard"
}

@test "setup claude is out of scope" {
  mark_out_of_scope "Modifies Claude Code config"
}

@test "quick-start is out of scope" {
  mark_out_of_scope "Interactive onboarding wizard"
}

@test "upgrade is out of scope" {
  mark_out_of_scope "Self-upgrade modifies the binary"
}

@test "migrate is out of scope" {
  mark_out_of_scope "Config migration — destructive"
}

@test "completion is out of scope" {
  mark_out_of_scope "Shell completion generation"
}

@test "mcp is out of scope" {
  mark_out_of_scope "MCP server — long-running process"
}

@test "tui is out of scope" {
  mark_out_of_scope "Terminal UI — interactive"
}

@test "bonfire is out of scope" {
  mark_out_of_scope "Experimental — split-pane TUI"
}

@test "api is out of scope" {
  mark_out_of_scope "Raw API passthrough — tested via specific commands"
}

@test "skill install is out of scope" {
  mark_out_of_scope "Modifies Claude Code config"
}
