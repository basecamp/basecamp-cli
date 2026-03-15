#!/usr/bin/env bash
# smoke_provision.sh — Enable disabled dock tools and create missing data
# resources. Writes QA_* environment variables to $1 for test files to source.
#
# Failures are non-fatal — if a resource can't be provisioned, the
# corresponding ensure_* helper will mark the test unverifiable at runtime.
#
# This script deliberately calls CLI commands (todolists create, etc.) that
# are also under test. That's acceptable: provisioning is setup, not testing.
# A regression in a create command will cause both provisioning and the
# corresponding write test to fail independently.

set -eo pipefail

ENV_FILE="${1:?Usage: smoke_provision.sh <env-file>}"

# --- Account and project discovery ---

QA_ACCOUNT="${BASECAMP_ACCOUNT_ID:-}"
if [[ -z "$QA_ACCOUNT" ]]; then
  out=$(basecamp accounts list --json 2>/dev/null) || { echo "Cannot list accounts" >&2; exit 1; }
  QA_ACCOUNT=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$QA_ACCOUNT" ]] || { echo "No accounts found" >&2; exit 1; }
fi
export BASECAMP_ACCOUNT_ID="$QA_ACCOUNT"

QA_PROJECT="${BASECAMP_PROJECT_ID:-}"
if [[ -z "$QA_PROJECT" ]]; then
  out=$(basecamp projects list --json 2>/dev/null) || { echo "Cannot list projects" >&2; exit 1; }
  QA_PROJECT=$(echo "$out" | jq -r '.data[0].id // empty')
  [[ -n "$QA_PROJECT" ]] || { echo "No projects found" >&2; exit 1; }
fi

# --- Read project dock once ---

dock_json=$(basecamp projects show "$QA_PROJECT" --json 2>/dev/null) || { echo "Cannot show project $QA_PROJECT" >&2; exit 1; }

# enable_dock_tool NAME — ensure a dock tool is enabled, print its ID.
# Finds by name (regardless of enabled state), enables idempotently.
enable_dock_tool() {
  local name="$1"
  local tool_id
  tool_id=$(echo "$dock_json" | jq -r "[.data.dock[]? | select(.name == \"$name\") | .id][0] // empty")
  if [[ -z "$tool_id" ]]; then
    return 1
  fi
  # Enable idempotently — already-enabled tools return success.
  basecamp tools enable "$tool_id" -p "$QA_PROJECT" --json >/dev/null 2>&1 || return 1
  echo "$tool_id"
}

# --- Enable dock tools ---

QA_CARDTABLE=$(enable_dock_tool "kanban_board") || QA_CARDTABLE=""
QA_CAMPFIRE=$(enable_dock_tool "chat") || QA_CAMPFIRE=""
QA_MESSAGEBOARD=$(enable_dock_tool "message_board") || QA_MESSAGEBOARD=""
QA_SCHEDULE=$(enable_dock_tool "schedule") || QA_SCHEDULE=""
QA_QUESTIONNAIRE=$(enable_dock_tool "questionnaire") || QA_QUESTIONNAIRE=""
QA_INBOX=$(enable_dock_tool "inbox") || QA_INBOX=""

# --- Create missing data resources ---

# Todolist
out=$(basecamp todolists list -p "$QA_PROJECT" --json 2>/dev/null) || out=""
QA_TODOLIST=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
if [[ -z "$QA_TODOLIST" ]]; then
  out=$(basecamp todolists create "Smoke Test List" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_TODOLIST=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
fi

# Todo (needs todolist)
QA_TODO=""
if [[ -n "$QA_TODOLIST" ]]; then
  out=$(basecamp todos list -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_TODO=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_TODO" ]]; then
    out=$(basecamp todos create "Smoke test todo" --list "$QA_TODOLIST" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
    QA_TODO=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
  fi
fi

# Message (needs messageboard enabled)
out=$(basecamp messages list -p "$QA_PROJECT" --json 2>/dev/null) || out=""
QA_MESSAGE=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
if [[ -z "$QA_MESSAGE" && -n "$QA_MESSAGEBOARD" ]]; then
  out=$(basecamp messages create "Smoke test message" "Automated smoke test" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_MESSAGE=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
fi

# Comment (needs todo)
QA_COMMENT=""
if [[ -n "$QA_TODO" ]]; then
  out=$(basecamp comments list "$QA_TODO" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_COMMENT=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_COMMENT" ]]; then
    out=$(basecamp comments create "$QA_TODO" "Smoke test comment" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
    QA_COMMENT=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
  fi
fi

# Upload
out=$(basecamp uploads list -p "$QA_PROJECT" --json 2>/dev/null) || out=""
QA_UPLOAD=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
if [[ -z "$QA_UPLOAD" ]]; then
  tmpfile=$(mktemp)
  echo "smoke test upload $(date +%s)" > "$tmpfile"
  out=$(basecamp uploads create "$tmpfile" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  rm -f "$tmpfile"
  QA_UPLOAD=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
fi

# Card (needs cardtable)
QA_CARD=""
if [[ -n "$QA_CARDTABLE" ]]; then
  out=$(basecamp cards list --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_CARD=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_CARD" ]]; then
    out=$(basecamp cards create "Smoke card" --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
    QA_CARD=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
  fi
fi

# Column (needs cardtable)
QA_COLUMN=""
if [[ -n "$QA_CARDTABLE" ]]; then
  out=$(basecamp cards columns --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
  QA_COLUMN=$(echo "${out:-"{}"}" | jq -r '.data[0].id // empty')
  if [[ -z "$QA_COLUMN" ]]; then
    out=$(basecamp cards column create "Smoke column" --card-table "$QA_CARDTABLE" -p "$QA_PROJECT" --json 2>/dev/null) || out=""
    QA_COLUMN=$(echo "${out:-"{}"}" | jq -r '.data.id // empty')
  fi
fi

# --- Write env file ---

{
  echo "# Generated by smoke_provision.sh — do not edit"
  printf 'export QA_ACCOUNT=%q\n' "$QA_ACCOUNT"
  printf 'export QA_PROJECT=%q\n' "$QA_PROJECT"
  printf 'export BASECAMP_ACCOUNT_ID=%q\n' "$QA_ACCOUNT"
  printf 'export BASECAMP_PROJECT_ID=%q\n' "$QA_PROJECT"
  for var in QA_CARDTABLE QA_CAMPFIRE QA_MESSAGEBOARD QA_SCHEDULE QA_QUESTIONNAIRE QA_INBOX \
             QA_TODOLIST QA_TODO QA_MESSAGE QA_COMMENT QA_UPLOAD QA_CARD QA_COLUMN; do
    val="${!var:-}"
    [[ -n "$val" ]] && printf 'export %s=%q\n' "$var" "$val"
  done
} > "$ENV_FILE"

provisioned=$(grep -c '^export' "$ENV_FILE")
echo "Provisioned $provisioned variables to $ENV_FILE"
