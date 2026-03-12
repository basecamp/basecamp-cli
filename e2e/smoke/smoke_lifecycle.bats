#!/usr/bin/env bats
# smoke_lifecycle.bats - Level 3: Lifecycle operations
# These require ephemeral accounts or are destructive.

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
