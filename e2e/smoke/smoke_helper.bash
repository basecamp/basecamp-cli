#!/usr/bin/env bash
# smoke_helper.bash - Helpers for the pre-release smoke suite.
#
# Provides resource discovery (ensure_*), trace capture, and test state
# management (mark_unverifiable, mark_out_of_scope).
#
# Requires: BASECAMP_PROFILE or BASECAMP_TOKEN set in the environment.

# Stash env vars before loading test_helper, whose setup() unsets them.
# setup_extra (called at the end of each setup()) restores them per-test.
_SMOKE_TOKEN="${BASECAMP_TOKEN:-}"
_SMOKE_ACCOUNT_ID="${BASECAMP_ACCOUNT_ID:-}"
_SMOKE_PROJECT_ID="${BASECAMP_PROJECT_ID:-}"
_SMOKE_PROFILE="${BASECAMP_PROFILE:-}"
_SMOKE_LAUNCHPAD_URL="${BASECAMP_LAUNCHPAD_URL:-}"
_SMOKE_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"

# Load the base test helper for assertions
SMOKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
load "$SMOKE_DIR/../test_helper"

# Restore BASECAMP_* after test_helper's setup() clears them.
# This runs at the end of every per-test setup() via the setup_extra hook.
setup_extra() {
  if [[ -n "$_SMOKE_PROFILE" ]]; then
    export BASECAMP_PROFILE="$_SMOKE_PROFILE"
    # test_helper.bash sets HOME to a temp dir, so the profile's config
    # and credentials are invisible. Copy them into the temp HOME.
    if [[ -d "$_SMOKE_CONFIG_DIR" ]]; then
      cp -a "$_SMOKE_CONFIG_DIR/." "$HOME/.config/basecamp/"
    fi
  fi
  if [[ -n "$_SMOKE_TOKEN" ]]; then
    export BASECAMP_TOKEN="$_SMOKE_TOKEN"
  fi
  if [[ -n "$_SMOKE_ACCOUNT_ID" ]]; then
    export BASECAMP_ACCOUNT_ID="$_SMOKE_ACCOUNT_ID"
  fi
  if [[ -n "$_SMOKE_PROJECT_ID" ]]; then
    export BASECAMP_PROJECT_ID="$_SMOKE_PROJECT_ID"
  fi
  if [[ -n "$_SMOKE_LAUNCHPAD_URL" ]]; then
    export BASECAMP_LAUNCHPAD_URL="$_SMOKE_LAUNCHPAD_URL"
  fi
}


# --- Test state management ---

# mark_unverifiable REASON — required data missing, test cannot run.
# Counts as a gap in the QA report (yellow). Blocks release unless allowlisted.
mark_unverifiable() {
  local reason="$1"
  write_trace "$BATS_TEST_NAME" "" 0 "unverifiable" "$reason"
  skip "$reason"
}

# mark_out_of_scope REASON — intentionally excluded (auth flows, interactive).
# Does not block release (gray in report).
mark_out_of_scope() {
  local reason="$1"
  write_trace "$BATS_TEST_NAME" "" 0 "out-of-scope" "$reason"
  skip "$reason"
}


# --- Trace capture ---
#
# Traces record gap/exclusion metadata only (unverifiable, out-of-scope).
# Pass/fail is determined by bats exit codes, not traces.
# The orchestrator (run_smoke.sh) gates on both: bats exit codes for
# functional correctness, traces for coverage gap tracking.

QA_TRACE_DIR="${QA_TRACE_DIR:-}"

# write_trace TEST COMMAND EXIT_CODE STATUS [REASON]
write_trace() {
  [[ -n "$QA_TRACE_DIR" ]] || return 0

  local test_name="$1"
  local command="$2"
  local exit_code="$3"
  local trace_status="$4"
  local reason="${5:-}"

  mkdir -p "$QA_TRACE_DIR"
  jq -n --compact-output \
    --arg test "$test_name" \
    --arg cmd "$command" \
    --argjson exit "$exit_code" \
    --arg status "$trace_status" \
    --arg reason "$reason" \
    '{test: $test, command: $cmd, exit_code: $exit, status: $status, reason: $reason}' \
    >> "$QA_TRACE_DIR/traces.jsonl"
}

# run_smoke COMMAND [ARGS...] — alias for `run` in smoke tests.
# No trace is written here — pass/fail comes from bats exit codes.
run_smoke() {
  run "$@"
}


# --- Resource discovery ---
#
# Each ensure_* is idempotent. Exports a QA_* variable.
# Discovery uses direct API calls to avoid running the exact CLI commands
# that the smoke tests are verifying (prevents regressions from being
# masked as "unverifiable").

ensure_token() {
  # Profile-based auth carries the token implicitly
  if [[ -n "${BASECAMP_PROFILE:-}" ]]; then
    return 0
  fi
  if [[ -z "${BASECAMP_TOKEN:-}" ]]; then
    echo "BASECAMP_PROFILE or BASECAMP_TOKEN required for smoke tests" >&2
    return 1
  fi
  export BASECAMP_TOKEN
}

ensure_account() {
  [[ -n "${QA_ACCOUNT:-}" ]] && return 0
  ensure_token || return 1

  # Use BASECAMP_ACCOUNT_ID if already set (avoids running `accounts list`)
  if [[ -n "${BASECAMP_ACCOUNT_ID:-}" ]]; then
    QA_ACCOUNT="$BASECAMP_ACCOUNT_ID"
    export QA_ACCOUNT
    return 0
  fi

  local out
  out=$(basecamp accounts list --json 2>/dev/null) || {
    mark_unverifiable "Cannot discover accounts"
    return 1
  }
  QA_ACCOUNT=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_ACCOUNT" ]]; then
    mark_unverifiable "No accounts found"
    return 1
  fi
  export QA_ACCOUNT

  # Propagate to BASECAMP_ACCOUNT_ID so CLI commands pick it up
  # without --account flags, and update the stash so setup_extra
  # restores it after test_helper's setup() clears the env.
  export BASECAMP_ACCOUNT_ID="$QA_ACCOUNT"
  _SMOKE_ACCOUNT_ID="$QA_ACCOUNT"
}

ensure_project() {
  [[ -n "${QA_PROJECT:-}" ]] && return 0
  ensure_account || return 1

  # Use BASECAMP_PROJECT_ID if already set (avoids running `projects list`)
  if [[ -n "${BASECAMP_PROJECT_ID:-}" ]]; then
    QA_PROJECT="$BASECAMP_PROJECT_ID"
    export QA_PROJECT
    return 0
  fi

  local out
  out=$(basecamp projects list --json 2>/dev/null) || {
    mark_unverifiable "Cannot discover projects"
    return 1
  }
  QA_PROJECT=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_PROJECT" ]]; then
    mark_unverifiable "No projects found"
    return 1
  fi
  export QA_PROJECT
}

ensure_todolist() {
  [[ -n "${QA_TODOLIST:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp todolists list -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list todolists in project $QA_PROJECT"
    return 1
  }
  QA_TODOLIST=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_TODOLIST" ]]; then
    mark_unverifiable "No todolists in project $QA_PROJECT"
    return 1
  fi
  export QA_TODOLIST
}

ensure_vault() {
  [[ -n "${QA_VAULT:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp vaults list -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list vaults in project $QA_PROJECT"
    return 1
  }
  QA_VAULT=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_VAULT" ]]; then
    mark_unverifiable "No vaults in project $QA_PROJECT"
    return 1
  fi
  export QA_VAULT
}

ensure_messageboard() {
  [[ -n "${QA_MESSAGEBOARD:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp messageboards show -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot find message board in project $QA_PROJECT"
    return 1
  }
  QA_MESSAGEBOARD=$(echo "$out" | jq -r '.data.id // empty')
  if [[ -z "$QA_MESSAGEBOARD" ]]; then
    mark_unverifiable "No message board in project $QA_PROJECT"
    return 1
  fi
  export QA_MESSAGEBOARD
}

ensure_cardtable() {
  [[ -n "${QA_CARDTABLE:-}" ]] && return 0
  ensure_project || return 1

  # Detect card table from the project dock (kanban_board) rather than
  # requiring existing cards. This matches getCardTableID in cards.go.
  local out
  out=$(basecamp projects show "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot show project $QA_PROJECT"
    return 1
  }
  QA_CARDTABLE=$(echo "$out" | jq -r '[.data.dock[]? | select(.name == "kanban_board" and .enabled == true) | .id][0] // empty')
  if [[ -z "$QA_CARDTABLE" ]]; then
    mark_unverifiable "No card table in project $QA_PROJECT dock"
    return 1
  fi
  export QA_CARDTABLE
}

ensure_campfire() {
  [[ -n "${QA_CAMPFIRE:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp projects show "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot show project $QA_PROJECT"
    return 1
  }
  QA_CAMPFIRE=$(echo "$out" | jq -r '[.data.dock[]? | select(.name == "chat" and .enabled == true) | .id][0] // empty')
  if [[ -z "$QA_CAMPFIRE" ]]; then
    mark_unverifiable "No campfire in project $QA_PROJECT dock"
    return 1
  fi
  export QA_CAMPFIRE
}

ensure_todo() {
  [[ -n "${QA_TODO:-}" ]] && return 0
  ensure_project || return 1
  ensure_todolist || return 1

  local out
  out=$(basecamp todos list -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list todos in project $QA_PROJECT"
    return 1
  }
  QA_TODO=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_TODO" ]]; then
    mark_unverifiable "No todos in project $QA_PROJECT"
    return 1
  fi
  export QA_TODO
}

ensure_message() {
  [[ -n "${QA_MESSAGE:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp messages list -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list messages in project $QA_PROJECT"
    return 1
  }
  QA_MESSAGE=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_MESSAGE" ]]; then
    mark_unverifiable "No messages in project $QA_PROJECT"
    return 1
  fi
  export QA_MESSAGE
}

ensure_person() {
  [[ -n "${QA_PERSON:-}" ]] && return 0
  ensure_token || return 1

  local out
  out=$(basecamp people list --json 2>/dev/null) || {
    mark_unverifiable "Cannot list people"
    return 1
  }
  QA_PERSON=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_PERSON" ]]; then
    mark_unverifiable "No people found"
    return 1
  fi
  export QA_PERSON
}

ensure_schedule() {
  [[ -n "${QA_SCHEDULE:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp schedule info -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot show schedule in project $QA_PROJECT"
    return 1
  }
  QA_SCHEDULE=$(echo "$out" | jq -r '.data.id // empty')
  if [[ -z "$QA_SCHEDULE" ]]; then
    mark_unverifiable "No schedule in project $QA_PROJECT"
    return 1
  fi
  export QA_SCHEDULE
}

ensure_questionnaire() {
  [[ -n "${QA_QUESTIONNAIRE:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp projects show "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot show project $QA_PROJECT"
    return 1
  }
  QA_QUESTIONNAIRE=$(echo "$out" | jq -r '[.data.dock[]? | select(.name == "questionnaire" and .enabled == true) | .id][0] // empty')
  if [[ -z "$QA_QUESTIONNAIRE" ]]; then
    mark_unverifiable "No questionnaire in project $QA_PROJECT dock"
    return 1
  fi
  export QA_QUESTIONNAIRE
}

ensure_inbox() {
  [[ -n "${QA_INBOX:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp projects show "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot show project $QA_PROJECT"
    return 1
  }
  QA_INBOX=$(echo "$out" | jq -r '[.data.dock[]? | select(.name == "inbox" and .enabled == true) | .id][0] // empty')
  if [[ -z "$QA_INBOX" ]]; then
    mark_unverifiable "No inbox in project $QA_PROJECT dock"
    return 1
  fi
  export QA_INBOX
}

ensure_card() {
  [[ -n "${QA_CARD:-}" ]] && return 0
  ensure_project || return 1
  ensure_cardtable || return 1

  local out
  out=$(basecamp cards list --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list cards in project $QA_PROJECT"
    return 1
  }
  QA_CARD=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_CARD" ]]; then
    mark_unverifiable "No cards in project $QA_PROJECT"
    return 1
  fi
  export QA_CARD
}

ensure_column() {
  [[ -n "${QA_COLUMN:-}" ]] && return 0
  ensure_project || return 1
  ensure_cardtable || return 1

  local out
  out=$(basecamp cards columns --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list columns in project $QA_PROJECT"
    return 1
  }
  QA_COLUMN=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_COLUMN" ]]; then
    mark_unverifiable "No columns in project $QA_PROJECT"
    return 1
  fi
  export QA_COLUMN
}

ensure_comment() {
  [[ -n "${QA_COMMENT:-}" ]] && return 0
  ensure_project || return 1
  ensure_todo || return 1

  local out
  out=$(basecamp comments list "$QA_TODO" -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list comments on todo $QA_TODO"
    return 1
  }
  QA_COMMENT=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_COMMENT" ]]; then
    mark_unverifiable "No comments on todo $QA_TODO"
    return 1
  fi
  export QA_COMMENT
}

ensure_upload() {
  [[ -n "${QA_UPLOAD:-}" ]] && return 0
  ensure_project || return 1

  local out
  out=$(basecamp uploads list -p "$QA_PROJECT" --json 2>/dev/null) || {
    mark_unverifiable "Cannot list uploads in project $QA_PROJECT"
    return 1
  }
  QA_UPLOAD=$(echo "$out" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_UPLOAD" ]]; then
    mark_unverifiable "No uploads in project $QA_PROJECT"
    return 1
  fi
  export QA_UPLOAD
}
