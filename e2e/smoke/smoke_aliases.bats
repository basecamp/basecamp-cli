#!/usr/bin/env bats
# smoke_aliases.bats - Coverage for command aliases and sub-aliases.
#
# The .surface file registers every alias as its own CMD entry. This file
# marks them out of scope since the canonical forms are tested elsewhere.
# Group-level OOS tests propagate to all leaf descendants automatically.

load smoke_helper

# ---------------------------------------------------------------------------
# Top-level command aliases (singular, shortened, or alternate names)
# ---------------------------------------------------------------------------

@test "boosts is out of scope" {
  mark_out_of_scope "Alias for boost — tested via canonical form"
}

@test "campfire is out of scope" {
  mark_out_of_scope "Alias for chat — tested via canonical form"
}

@test "chat show is out of scope" {
  mark_out_of_scope "Alias for chat line — tested via canonical form"
}

@test "checkin is out of scope" {
  mark_out_of_scope "Alias for checkins — tested via canonical form"
}

@test "cmds is out of scope" {
  mark_out_of_scope "Alias for commands — tested via canonical form"
}

@test "msgs is out of scope" {
  mark_out_of_scope "Alias for messages — tested via canonical form"
}

@test "project is out of scope" {
  mark_out_of_scope "Alias for projects — tested via canonical form"
}

@test "todolist is out of scope" {
  mark_out_of_scope "Alias for todolists — tested via canonical form"
}

@test "todolistgroup is out of scope" {
  mark_out_of_scope "Alias for todolistgroups — tested via canonical form"
}

@test "tlgroup is out of scope" {
  mark_out_of_scope "Alias for todolistgroups — tested via canonical form"
}

@test "tlgroups is out of scope" {
  mark_out_of_scope "Alias for todolistgroups — tested via canonical form"
}

@test "webhook is out of scope" {
  mark_out_of_scope "Alias for webhooks — tested via canonical form"
}

# ---------------------------------------------------------------------------
# File-management root aliases (all share implementation with files/vaults)
# ---------------------------------------------------------------------------

@test "documents is out of scope" {
  mark_out_of_scope "Alias for files — tested via canonical form"
}

@test "file is out of scope" {
  mark_out_of_scope "Alias for files — tested via canonical form"
}

@test "folders is out of scope" {
  mark_out_of_scope "Alias for files — tested via canonical form"
}

@test "vault is out of scope" {
  mark_out_of_scope "Alias for vaults — tested via canonical form"
}

# ---------------------------------------------------------------------------
# File-management sub-aliases within canonical groups (docs, files, vaults)
#
# Each canonical group has sub-aliases like doc/document/folder/upload/vault
# that resolve to the canonical sub-group (documents/folders/uploads/vaults).
# Group-level OOS propagates to their create/list leaves.
# ---------------------------------------------------------------------------

# --- docs sub-aliases ---

@test "docs doc is out of scope" {
  mark_out_of_scope "Sub-alias for docs documents — tested via canonical form"
}

@test "docs document is out of scope" {
  mark_out_of_scope "Sub-alias for docs documents — tested via canonical form"
}

@test "docs folder is out of scope" {
  mark_out_of_scope "Sub-alias for docs folders — tested via canonical form"
}

@test "docs upload is out of scope" {
  mark_out_of_scope "Sub-alias for docs uploads — tested via canonical form"
}

@test "docs vault is out of scope" {
  mark_out_of_scope "Sub-alias for docs vaults — tested via canonical form"
}

@test "docs vaults is out of scope" {
  mark_out_of_scope "Sub-alias for docs vaults — tested via canonical form"
}

# --- files sub-aliases ---

@test "files doc is out of scope" {
  mark_out_of_scope "Sub-alias for files documents — tested via canonical form"
}

@test "files document is out of scope" {
  mark_out_of_scope "Sub-alias for files documents — tested via canonical form"
}

@test "files folder is out of scope" {
  mark_out_of_scope "Sub-alias for files folders — tested via canonical form"
}

@test "files upload is out of scope" {
  mark_out_of_scope "Sub-alias for files uploads — tested via canonical form"
}

@test "files vault is out of scope" {
  mark_out_of_scope "Sub-alias for files vaults — tested via canonical form"
}

@test "files vaults is out of scope" {
  mark_out_of_scope "Sub-alias for files vaults — tested via canonical form"
}

# --- vaults sub-aliases ---

@test "vaults doc is out of scope" {
  mark_out_of_scope "Sub-alias for vaults documents — tested via canonical form"
}

@test "vaults document is out of scope" {
  mark_out_of_scope "Sub-alias for vaults documents — tested via canonical form"
}

@test "vaults folder is out of scope" {
  mark_out_of_scope "Sub-alias for vaults folders — tested via canonical form"
}

@test "vaults upload is out of scope" {
  mark_out_of_scope "Sub-alias for vaults uploads — tested via canonical form"
}

@test "vaults vault is out of scope" {
  mark_out_of_scope "Sub-alias for vaults vaults — tested via canonical form"
}

@test "vaults vaults is out of scope" {
  mark_out_of_scope "Sub-alias for vaults vaults — tested via canonical form"
}

# ---------------------------------------------------------------------------
# Leaf command aliases (individual subcommands with alternate names)
# ---------------------------------------------------------------------------

# --- cards ---

@test "card mv is out of scope" {
  mark_out_of_scope "Alias for card move — tested via canonical form"
}

@test "cards mv is out of scope" {
  mark_out_of_scope "Alias for cards move — tested via canonical form"
}

# --- recordings ---

@test "recordings active is out of scope" {
  mark_out_of_scope "Alias for recordings restore — tested via canonical form"
}

@test "recordings archived is out of scope" {
  mark_out_of_scope "Alias for recordings archive — tested via canonical form"
}

@test "recordings client-visibility is out of scope" {
  mark_out_of_scope "Alias for recordings visibility — tested via canonical form"
}

@test "recordings trashed is out of scope" {
  mark_out_of_scope "Alias for recordings trash — tested via canonical form"
}

# --- search ---

@test "search types is out of scope" {
  mark_out_of_scope "Alias for search metadata — tested via canonical form"
}

# --- timesheet ---

@test "timesheet recording is out of scope" {
  mark_out_of_scope "Alias for timesheet item — tested via canonical form"
}

# --- todos ---

@test "todos move is out of scope" {
  mark_out_of_scope "Alias for todos position — tested via canonical form"
}

@test "todos reopen is out of scope" {
  mark_out_of_scope "Alias for todos uncomplete — tested via canonical form"
}

@test "todos reorder is out of scope" {
  mark_out_of_scope "Alias for todos position — tested via canonical form"
}

# --- todolistgroups ---

@test "todolistgroups move is out of scope" {
  mark_out_of_scope "Alias for todolistgroups position — tested via canonical form"
}

@test "todolistgroups rename is out of scope" {
  mark_out_of_scope "Alias for todolistgroups update — tested via canonical form"
}

# --- tools ---

@test "tools delete is out of scope" {
  mark_out_of_scope "Alias for tools trash — tested via canonical form"
}

@test "tools move is out of scope" {
  mark_out_of_scope "Alias for tools reposition — tested via canonical form"
}

@test "tools rename is out of scope" {
  mark_out_of_scope "Alias for tools update — tested via canonical form"
}

# ---------------------------------------------------------------------------
# New commands needing OOS (destructive or unsafe in smoke environment)
# ---------------------------------------------------------------------------

@test "projects trash is out of scope" {
  mark_out_of_scope "Soft-trashes a project — destructive mutation"
}
