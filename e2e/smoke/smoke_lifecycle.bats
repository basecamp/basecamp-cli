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

# --- Profile mutations (require interactive OAuth / confirmation prompts) ---

@test "profile create is out of scope" {
  mark_out_of_scope "Triggers OAuth flow — no non-interactive mode"
}

@test "profile delete is out of scope" {
  mark_out_of_scope "Interactive confirmation prompt — no --force flag"
}

@test "profile set-default is out of scope" {
  mark_out_of_scope "Depends on profile create (OOS)"
}

# --- Account-wide / dangerous mutations ---

@test "templates construct is out of scope" {
  mark_out_of_scope "Creates project from template — account-wide mutation"
}

@test "templates construction is out of scope" {
  mark_out_of_scope "Depends on templates construct (OOS)"
}

@test "templates create is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates update is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates delete is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "messagetypes create is out of scope" {
  mark_out_of_scope "Account-wide message type mutation"
}

@test "messagetypes update is out of scope" {
  mark_out_of_scope "Account-wide message type mutation"
}

@test "messagetypes delete is out of scope" {
  mark_out_of_scope "Account-wide message type mutation"
}

@test "people add is out of scope" {
  mark_out_of_scope "Modifies project membership"
}

@test "people remove is out of scope" {
  mark_out_of_scope "Modifies project membership"
}

@test "todos sweep is out of scope" {
  mark_out_of_scope "Bulk completion — destructive, no undo"
}

# --- Code-path equivalence: docs group shares implementation with files ---

@test "docs archive is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs documents list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs folders create is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs folders list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs restore is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs trash is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs update is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs uploads create is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs uploads list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

# --- Code-path equivalence: vaults group shares implementation with files ---

@test "vaults archive is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults documents create is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults documents list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults download is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults folders list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults restore is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults trash is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults update is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults uploads create is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults uploads list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}
