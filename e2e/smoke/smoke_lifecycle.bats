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

@test "auth agent connect is out of scope" {
  mark_out_of_scope "Waits on a person approving the connection in a browser"
}

@test "connect setup is out of scope" {
  mark_out_of_scope "Needs a connected agent profile and writes local connector policy — covered by Go tests in internal/commands and internal/connector/setup"
}

@test "connect show is out of scope" {
  mark_out_of_scope "Reads the connector policy a connected profile's setup wrote — covered by Go tests in internal/commands"
}

@test "connect status is out of scope" {
  mark_out_of_scope "Reads a local connector ledger the smoke account does not have — covered by Go tests in internal/commands and internal/connector"
}

@test "connect doctor is out of scope" {
  mark_out_of_scope "Needs a set-up connector profile and starts its MCP server — covered by Go tests in internal/commands"
}

@test "connect redispatch is out of scope" {
  mark_out_of_scope "Decides a record in a local connector ledger — covered by Go tests in internal/commands and internal/connector"
}

@test "connect discard is out of scope" {
  mark_out_of_scope "Decides a record in a local connector ledger — covered by Go tests in internal/commands and internal/connector"
}

@test "connect release is out of scope" {
  mark_out_of_scope "Clears the hold in a local connector ledger — covered by Go tests in internal/commands and internal/connector"
}

@test "connect shadow promote is out of scope" {
  mark_out_of_scope "Moves a local shadow ledger — covered by Go tests, including a process killed at every step, in internal/connector"
}

@test "connect import is out of scope" {
  mark_out_of_scope "Applies a reconciliation file to a local connector ledger — covered by Go tests in internal/commands and internal/connector"
}

@test "connect service install is out of scope" {
  mark_out_of_scope "Writes a systemd user unit and asks systemctl to start it, which the smoke runner has no session for — covered by Go tests in internal/commands"
}

@test "connect service uninstall is out of scope" {
  mark_out_of_scope "Stops a systemd user unit and removes it, which the smoke runner has no session for — covered by Go tests in internal/commands"
}

@test "auth refresh is out of scope" {
  mark_out_of_scope "Requires OAuth credentials"
}

@test "auth revoke is out of scope" {
  mark_out_of_scope "Revokes the account's OAuth credentials"
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

@test "setup agents is out of scope" {
  mark_out_of_scope "Modifies coding-agent config"
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


@test "tui is out of scope" {
  mark_out_of_scope "Terminal UI — interactive"
}

@test "bonfire is out of scope" {
  mark_out_of_scope "Experimental — split-pane TUI"
}

@test "api is out of scope" {
  mark_out_of_scope "Raw API passthrough — tested via specific commands"
}

@test "mcp is out of scope" {
  mark_out_of_scope "Long-running MCP stdio server — covered by Go wire tests in internal/mcpserver and internal/commands"
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

@test "templates projects construct is out of scope" {
  mark_out_of_scope "Creates project from template — account-wide mutation"
}

@test "templates projects construction is out of scope" {
  mark_out_of_scope "Depends on templates projects construct (OOS)"
}

@test "templates projects create is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates projects update is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates projects delete is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates todolists duplicate is out of scope" {
  mark_out_of_scope "Duplicates a to-do list into a project and may grant project access"
}

@test "templates todolists duplication is out of scope" {
  mark_out_of_scope "Depends on templates todolists duplicate (OOS)"
}

@test "templates todolists copy is out of scope" {
  mark_out_of_scope "Alias of templates todolists duplicate (OOS)"
}

@test "templates todolists copy-status is out of scope" {
  mark_out_of_scope "Alias of templates todolists duplication (OOS)"
}

@test "templates todolists archive is out of scope" {
  mark_out_of_scope "Takes a template out of the account library"
}

@test "templates todolists trash is out of scope" {
  mark_out_of_scope "Takes a template out of the account library"
}

@test "templates todolists restore is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates card-tables create is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates card-tables duplicate is out of scope" {
  mark_out_of_scope "Duplicates a board into a project and may grant project access"
}

@test "templates card-tables duplication is out of scope" {
  mark_out_of_scope "Depends on templates card-tables duplicate (OOS)"
}

@test "templates card-tables copy is out of scope" {
  mark_out_of_scope "Alias of templates card-tables duplicate (OOS)"
}

@test "templates card-tables copy-status is out of scope" {
  mark_out_of_scope "Alias of templates card-tables duplication (OOS)"
}

@test "templates card-tables archive is out of scope" {
  mark_out_of_scope "Takes a template out of the account library"
}

@test "templates card-tables trash is out of scope" {
  mark_out_of_scope "Takes a template out of the account library"
}

@test "templates card-tables restore is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "templates todolists create is out of scope" {
  mark_out_of_scope "Account-wide template mutation"
}

@test "todolists templatify is out of scope" {
  mark_out_of_scope "Writes a new template into the account library"
}

@test "todolists templatification is out of scope" {
  mark_out_of_scope "Depends on todolists templatify (OOS)"
}

@test "card-tables templatify is out of scope" {
  mark_out_of_scope "Writes a new template into the account library"
}

@test "card-tables templatification is out of scope" {
  mark_out_of_scope "Depends on card-tables templatify (OOS)"
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

@test "people clients add is out of scope" {
  mark_out_of_scope "Modifies project membership"
}

@test "people clients remove is out of scope" {
  mark_out_of_scope "Modifies project membership"
}

@test "people clients invite is out of scope" {
  mark_out_of_scope "Invites a new client by email — consumes an account seat"
}

@test "people clients enable is out of scope" {
  mark_out_of_scope "Reconfigures project-wide client visibility"
}

@test "people clients disable is out of scope" {
  mark_out_of_scope "Reconfigures project-wide client visibility"
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

@test "docs versions is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs replace is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "docs new-version is out of scope" {
  mark_out_of_scope "Alias of files replace (tested)"
}

@test "files new-version is out of scope" {
  mark_out_of_scope "Alias of files replace (tested)"
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

@test "vaults versions is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults replace is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults new-version is out of scope" {
  mark_out_of_scope "Alias of files replace (tested)"
}

@test "vaults uploads create is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}

@test "vaults uploads list is out of scope" {
  mark_out_of_scope "Shares implementation with files group (tested)"
}
