#!/usr/bin/env bash
# Create benchmark fixtures
# Idempotent: safe to run multiple times

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$BENCH_DIR/env.sh"

FIXTURES_FILE="$BENCH_DIR/.fixtures.json"
PROJECT_NAME="bcq Benchmark Project"
PROJECT_NAME_2="bcq Benchmark Project B"  # Second project for Task 12
TODOLIST_NAME="Benchmark Todolist"
SEED_TODOS=75
OVERDUE_COUNT=3

# Read fixtures from spec.yaml (canonical source of truth)
SEARCH_MARKER=$(yq -r '.fixtures.search_marker' "$BENCH_DIR/spec.yaml")
MALICIOUS_MESSAGE_SUBJECT=$(yq -r '.fixtures.malicious_message_subject' "$BENCH_DIR/spec.yaml")
MALICIOUS_MESSAGE_CONTENT=$(yq -r '.fixtures.malicious_message_content' "$BENCH_DIR/spec.yaml")

log() { echo "[seed] $*" >&2; }
die() { echo "[seed] ERROR: $*" >&2; exit 1; }

# Helper to get all todos from a todolist (bcq now handles pagination)
get_all_todos() {
  local project_id="$1"
  local todolist_id="$2"
  bcq --json todos --in "$project_id" --list "$todolist_id" | jq -r '.data'
}

# Initialize fixtures object
declare -A fixtures

# Find or create benchmark project
find_or_create_project() {
  log "Looking for benchmark project..."

  local project_id
  project_id=$(bcq --json projects | jq -r --arg name "$PROJECT_NAME" \
    '.data[] | select(.name == $name) | .id' | head -1)

  if [[ -n "$project_id" ]]; then
    log "Found existing project: $project_id"
    fixtures[project_id]="$project_id"
    return 0
  fi

  log "Creating benchmark project..."
  local result
  result=$(bcq --json projects create "$PROJECT_NAME")
  project_id=$(echo "$result" | jq -r '.data.id')

  if [[ -z "$project_id" ]] || [[ "$project_id" == "null" ]]; then
    die "Failed to create project"
  fi

  log "Created project: $project_id"
  fixtures[project_id]="$project_id"
}

# Get todoset ID for the project
get_todoset() {
  log "Getting todoset for project..."

  local todoset_id
  todoset_id=$(bcq --json todosets --project "${fixtures[project_id]}" | jq -r '.data.id')

  if [[ -z "$todoset_id" ]] || [[ "$todoset_id" == "null" ]]; then
    die "Failed to get todoset"
  fi

  log "Todoset ID: $todoset_id"
  fixtures[todoset_id]="$todoset_id"
}

# Get messageboard ID for the project
get_messageboard() {
  log "Getting messageboard for project..."

  local messageboard_id
  messageboard_id=$(bcq --json messageboards --project "${fixtures[project_id]}" | jq -r '.data.id')

  if [[ -z "$messageboard_id" ]] || [[ "$messageboard_id" == "null" ]]; then
    die "Failed to get messageboard"
  fi

  log "Messageboard ID: $messageboard_id"
  fixtures[messageboard_id]="$messageboard_id"
}

# Find or create benchmark todolist
find_or_create_todolist() {
  log "Looking for benchmark todolist..."

  local todolist_id
  todolist_id=$(bcq --json todolists --project "${fixtures[project_id]}" | jq -r --arg name "$TODOLIST_NAME" \
    '.data[] | select(.name == $name) | .id' | head -1)

  if [[ -n "$todolist_id" ]]; then
    log "Found existing todolist: $todolist_id"
    fixtures[todolist_id]="$todolist_id"
    return 0
  fi

  log "Creating benchmark todolist..."
  local result
  result=$(bcq --json todolists create "$TODOLIST_NAME" --project "${fixtures[project_id]}")
  todolist_id=$(echo "$result" | jq -r '.data.id')

  if [[ -z "$todolist_id" ]] || [[ "$todolist_id" == "null" ]]; then
    die "Failed to create todolist"
  fi

  log "Created todolist: $todolist_id"
  fixtures[todolist_id]="$todolist_id"
}

# Seed todos in the todolist
# Creates exactly SEED_TODOS todos numbered 1 through SEED_TODOS
# Gap-aware: fills missing indices rather than just appending
seed_todos() {
  log "Checking existing seed todos..."

  local project_id="${fixtures[project_id]}"
  local todolist_id="${fixtures[todolist_id]}"

  # Get all todos and filter to seeds
  local all_todos
  all_todos=$(get_all_todos "$project_id" "$todolist_id")

  # Get list of existing seed todo numbers
  local existing_nums
  existing_nums=$(echo "$all_todos" | jq -r '[.[] | select(.content | startswith("Benchmark Seed Todo")) | .content | capture("Benchmark Seed Todo (?<n>[0-9]+)") | .n | tonumber] | sort | .[]')

  local existing_count
  existing_count=$(echo "$existing_nums" | grep -c . 2>/dev/null || true)
  existing_count=${existing_count:-0}

  if [[ "$existing_count" -ge "$SEED_TODOS" ]]; then
    log "Already have $existing_count seed todos (need $SEED_TODOS)"
    return 0
  fi

  # Find missing numbers in range 1..SEED_TODOS
  local missing_nums=()
  for i in $(seq 1 "$SEED_TODOS"); do
    if ! echo "$existing_nums" | grep -qx "$i"; then
      missing_nums+=("$i")
    fi
  done

  local to_create=${#missing_nums[@]}
  if [[ "$to_create" -eq 0 ]]; then
    log "All seed todos 1-$SEED_TODOS exist"
    return 0
  fi

  log "Creating $to_create seed todos (filling gaps: ${missing_nums[*]:0:5}...)..."

  for num in "${missing_nums[@]}"; do
    bcq todo "Benchmark Seed Todo $num" \
      --project "$project_id" \
      --todolist "$todolist_id" \
      --json >/dev/null
    printf "."
  done
  echo ""
  log "Created $to_create seed todos (filled gaps in 1-$SEED_TODOS)"
}

# Create named todos for specific tests
create_named_todos() {
  local project_id="${fixtures[project_id]}"
  local todolist_id="${fixtures[todolist_id]}"

  # Get all todos once (bcq handles pagination)
  local all_todos
  all_todos=$(get_all_todos "$project_id" "$todolist_id")

  # Todo Alpha - for find/complete test
  log "Creating/finding Todo Alpha..."
  local alpha_id
  alpha_id=$(echo "$all_todos" | jq -r '.[] | select(.content == "Benchmark Todo Alpha") | .id' | head -1)

  if [[ -z "$alpha_id" ]]; then
    local result
    result=$(bcq --json todo "Benchmark Todo Alpha" --project "$project_id" --todolist "$todolist_id")
    alpha_id=$(echo "$result" | jq -r '.data.id')
    log "Created Todo Alpha: $alpha_id"
  else
    log "Found Todo Alpha: $alpha_id"
    # Ensure it's not completed
    bcq --json reopen "$alpha_id" --project "$project_id" >/dev/null 2>&1 || true
  fi
  fixtures[todo_alpha_id]="$alpha_id"

  # Todo Beta - for reorder test (create at end so it's NOT first)
  log "Creating/finding Todo Beta..."
  local beta_id
  beta_id=$(echo "$all_todos" | jq -r '.[] | select(.content == "Benchmark Todo Beta") | .id' | head -1)

  if [[ -z "$beta_id" ]]; then
    local result
    result=$(bcq --json todo "Benchmark Todo Beta" --project "$project_id" --todolist "$todolist_id")
    beta_id=$(echo "$result" | jq -r '.data.id')
    log "Created Todo Beta: $beta_id"
  else
    log "Found Todo Beta: $beta_id"
  fi
  fixtures[todo_beta_id]="$beta_id"

  # Search marker todo - unique marker for deterministic search validation
  log "Creating/finding Search Marker todo..."
  local marker_content="Search marker $SEARCH_MARKER for validation"
  local marker_id
  marker_id=$(echo "$all_todos" | jq -r --arg c "$marker_content" '.[] | select(.content == $c) | .id' | head -1)

  if [[ -z "$marker_id" ]]; then
    local result
    result=$(bcq --json todo "$marker_content" --project "$project_id" --todolist "$todolist_id")
    marker_id=$(echo "$result" | jq -r '.data.id')
    log "Created Search Marker: $marker_id"
  else
    log "Found Search Marker: $marker_id"
  fi
}

# Create overdue todos for bulk complete test
# Creates exactly OVERDUE_COUNT todos with due_on = yesterday
# For existing todos: updates due_on to yesterday and reopens if completed
# Stores IDs in fixtures[overdue_ids] as comma-separated list
create_overdue_todos() {
  local project_id="${fixtures[project_id]}"
  local todolist_id="${fixtures[todolist_id]}"
  local yesterday
  yesterday=$(date -v-1d +%Y-%m-%d 2>/dev/null || date -d "yesterday" +%Y-%m-%d)

  log "Creating/updating overdue todos (due_on=$yesterday)..."

  # Get all todos once (bcq handles pagination)
  local all_todos
  all_todos=$(get_all_todos "$project_id" "$todolist_id")

  local overdue_ids=()

  for i in $(seq 1 $OVERDUE_COUNT); do
    local content="Benchmark Overdue Todo $i"
    local existing_id
    existing_id=$(echo "$all_todos" | jq -r --arg c "$content" '.[] | select(.content == $c) | .id' | head -1)

    if [[ -z "$existing_id" ]]; then
      # Create new and capture ID
      local result
      result=$(bcq todo "$content" \
        --project "$project_id" \
        --todolist "$todolist_id" \
        --due "$yesterday" \
        --json 2>/dev/null | sed -n '/^{/,$p')
      local new_id
      new_id=$(echo "$result" | jq -r '.data.id // .id // empty')
      if [[ -n "$new_id" ]]; then
        overdue_ids+=("$new_id")
        log "Created: $content ($new_id) due=$yesterday"
      else
        log "ERROR: Failed to create $content"
      fi
    else
      # Update existing: set due_on and reopen
      # Use raw API to ensure due_on is set (bcq may not have update command)
      local token
      token=$(_get_token_for_api)
      local api_base
      api_base=$(_get_api_base_for_raw)

      curl -s -X PUT \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/json" \
        -d "{\"due_on\":\"$yesterday\"}" \
        "$api_base/buckets/$project_id/todos/$existing_id.json" >/dev/null

      # Reopen if completed
      bcq --json reopen "$existing_id" --project "$project_id" >/dev/null 2>&1 || true
      overdue_ids+=("$existing_id")
      log "Updated: $content ($existing_id) due=$yesterday"
    fi
  done

  # Store comma-separated list of IDs
  fixtures[overdue_ids]=$(IFS=,; echo "${overdue_ids[*]}")
  log "Stored overdue IDs: ${fixtures[overdue_ids]}"
}

# Helper to get API token for raw curl calls
_get_token_for_api() {
  local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
  local creds_file="$config_dir/credentials.json"
  local config_file="$config_dir/config.json"

  if [[ ! -f "$creds_file" ]]; then
    echo "${BASECAMP_TOKEN:-}"
    return
  fi

  local base_url="${BCQ_BASE_URL:-}"
  if [[ -z "$base_url" ]] && [[ -f "$config_file" ]]; then
    base_url=$(jq -r '.base_url // empty' "$config_file")
  fi
  base_url="${base_url:-https://3.basecampapi.com}"
  base_url="${base_url%/}"

  jq -r --arg url "$base_url" '.[$url].access_token // empty' "$creds_file"
}

# Helper to get API base URL for raw curl calls
_get_api_base_for_raw() {
  local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/basecamp"
  local config_file="$config_dir/config.json"

  local api_url="${BCQ_API_URL:-}"
  if [[ -z "$api_url" ]] && [[ -f "$config_file" ]]; then
    api_url=$(jq -r '.api_url // empty' "$config_file")
  fi

  if [[ -n "$api_url" ]]; then
    echo "${api_url%/}/$BASECAMP_ACCOUNT_ID"
    return
  fi

  # Derive from base_url
  local base_url="${BCQ_BASE_URL:-}"
  if [[ -z "$base_url" ]] && [[ -f "$config_file" ]]; then
    base_url=$(jq -r '.base_url // empty' "$config_file")
  fi
  base_url="${base_url:-https://3.basecampapi.com}"
  base_url="${base_url%/}"

  # Convert basecamp to basecampapi
  local api_base
  api_base=$(echo "$base_url" | sed 's/basecamp\([^a-z]\)/basecampapi\1/; s/basecamp$/basecampapi/')
  echo "$api_base/$BASECAMP_ACCOUNT_ID"
}

# Create seeded message
create_seeded_message() {
  local project_id="${fixtures[project_id]}"
  local messageboard_id="${fixtures[messageboard_id]}"

  log "Creating/finding seeded message..."

  local message_id
  message_id=$(bcq --json messages --project "$project_id" | \
    jq -r '.data[] | select(.subject == "Benchmark Seeded Message") | .id' | head -1)

  if [[ -z "$message_id" ]]; then
    local result
    result=$(bcq message "Benchmark Seeded Message" \
      --project "$project_id" \
      --content "This message is used for benchmark testing." \
      --json)
    message_id=$(echo "$result" | jq -r '.data.id')
    log "Created message: $message_id"
  else
    log "Found message: $message_id"
  fi
  fixtures[message_id]="$message_id"
}

# Create malicious message for prompt injection test (Task 11)
create_malicious_message() {
  local project_id="${fixtures[project_id]}"

  log "Creating/finding malicious message for injection test..."

  local message_id
  message_id=$(bcq --json messages --project "$project_id" | \
    jq -r --arg subj "$MALICIOUS_MESSAGE_SUBJECT" '.data[] | select(.subject == $subj) | .id' | head -1)

  if [[ -z "$message_id" ]]; then
    local result
    result=$(bcq message "$MALICIOUS_MESSAGE_SUBJECT" \
      --project "$project_id" \
      --content "$MALICIOUS_MESSAGE_CONTENT" \
      --json)
    message_id=$(echo "$result" | jq -r '.data.id')
    log "Created malicious message: $message_id"
  else
    log "Found malicious message: $message_id"
  fi
  fixtures[malicious_message_id]="$message_id"
}

# Create second benchmark project for Task 12 (cross-project tests)
find_or_create_project_2() {
  log "Looking for second benchmark project..."

  local project_id
  project_id=$(bcq --json projects | jq -r --arg name "$PROJECT_NAME_2" \
    '.data[] | select(.name == $name) | .id' | head -1)

  if [[ -n "$project_id" ]]; then
    log "Found existing project 2: $project_id"
    fixtures[project_id_2]="$project_id"
    return 0
  fi

  log "Creating benchmark project 2..."
  local result
  result=$(bcq --json projects create "$PROJECT_NAME_2")
  project_id=$(echo "$result" | jq -r '.data.id')

  if [[ -z "$project_id" ]] || [[ "$project_id" == "null" ]]; then
    die "Failed to create project 2"
  fi

  log "Created project 2: $project_id"
  fixtures[project_id_2]="$project_id"
}

# Get todoset for project 2
get_todoset_2() {
  log "Getting todoset for project 2..."

  local todoset_id
  todoset_id=$(bcq --json todosets --project "${fixtures[project_id_2]}" | jq -r '.data.id')

  if [[ -z "$todoset_id" ]] || [[ "$todoset_id" == "null" ]]; then
    die "Failed to get todoset for project 2"
  fi

  log "Todoset 2 ID: $todoset_id"
  fixtures[todoset_id_2]="$todoset_id"
}

# Create todolist in project 2
find_or_create_todolist_2() {
  log "Looking for benchmark todolist in project 2..."

  local todolist_id
  todolist_id=$(bcq --json todolists --project "${fixtures[project_id_2]}" | jq -r --arg name "$TODOLIST_NAME" \
    '.data[] | select(.name == $name) | .id' | head -1)

  if [[ -n "$todolist_id" ]]; then
    log "Found existing todolist 2: $todolist_id"
    fixtures[todolist_id_2]="$todolist_id"
    return 0
  fi

  log "Creating benchmark todolist in project 2..."
  local result
  result=$(bcq --json todolists create "$TODOLIST_NAME" --project "${fixtures[project_id_2]}")
  todolist_id=$(echo "$result" | jq -r '.data.id')

  if [[ -z "$todolist_id" ]] || [[ "$todolist_id" == "null" ]]; then
    die "Failed to create todolist in project 2"
  fi

  log "Created todolist 2: $todolist_id"
  fixtures[todolist_id_2]="$todolist_id"
}

# Seed 75 todos in project 2 (forces pagination like project 1)
seed_todos_project_2() {
  log "Checking existing seed todos in project 2..."

  local project_id="${fixtures[project_id_2]}"
  local todolist_id="${fixtures[todolist_id_2]}"

  # Get all todos and filter to seeds
  local all_todos
  all_todos=$(get_all_todos "$project_id" "$todolist_id")

  # Get list of existing seed todo numbers
  local existing_nums
  existing_nums=$(echo "$all_todos" | jq -r '[.[] | select(.content | startswith("Benchmark Seed Todo")) | .content | capture("Benchmark Seed Todo (?<n>[0-9]+)") | .n | tonumber] | sort | .[]')

  local existing_count
  existing_count=$(echo "$existing_nums" | grep -c . 2>/dev/null || true)
  existing_count=${existing_count:-0}

  if [[ "$existing_count" -ge "$SEED_TODOS" ]]; then
    log "Already have $existing_count seed todos in P2 (need $SEED_TODOS)"
    return 0
  fi

  # Find missing numbers in range 1..SEED_TODOS
  local missing_nums=()
  for i in $(seq 1 "$SEED_TODOS"); do
    if ! echo "$existing_nums" | grep -qx "$i"; then
      missing_nums+=("$i")
    fi
  done

  local to_create=${#missing_nums[@]}
  if [[ "$to_create" -eq 0 ]]; then
    log "All seed todos 1-$SEED_TODOS exist in P2"
    return 0
  fi

  log "Creating $to_create seed todos in P2 (filling gaps: ${missing_nums[*]:0:5}...)..."

  for num in "${missing_nums[@]}"; do
    bcq todo "Benchmark Seed Todo $num" \
      --project "$project_id" \
      --todolist "$todolist_id" \
      --json >/dev/null 2>&1

    # Progress every 10
    if (( num % 10 == 0 )); then
      log "  Created $num/$SEED_TODOS..."
    fi
  done

  log "Seed todos in P2 complete"
}

# Create overdue todos in project 2
# Stores IDs in fixtures[overdue_ids_2] as comma-separated list
create_overdue_todos_project_2() {
  local project_id="${fixtures[project_id_2]}"
  local todolist_id="${fixtures[todolist_id_2]}"
  local yesterday
  yesterday=$(date -v-1d +%Y-%m-%d 2>/dev/null || date -d "yesterday" +%Y-%m-%d)

  log "Creating overdue todos in project 2 (due_on=$yesterday)..."

  # Get all todos in project 2
  local all_todos
  all_todos=$(get_all_todos "$project_id" "$todolist_id")

  local overdue_ids=()

  for i in $(seq 1 $OVERDUE_COUNT); do
    local content="Benchmark Overdue Todo $i"
    local existing_id
    existing_id=$(echo "$all_todos" | jq -r --arg c "$content" '.[] | select(.content == $c) | .id' | head -1)

    if [[ -z "$existing_id" ]]; then
      # Create new and capture ID
      local result
      result=$(bcq todo "$content" \
        --project "$project_id" \
        --todolist "$todolist_id" \
        --due "$yesterday" \
        --json 2>/dev/null | sed -n '/^{/,$p')
      local new_id
      new_id=$(echo "$result" | jq -r '.data.id // .id // empty')
      if [[ -n "$new_id" ]]; then
        overdue_ids+=("$new_id")
        log "Created in P2: $content ($new_id) due=$yesterday"
      else
        log "ERROR: Failed to create $content in P2"
      fi
    else
      # Update existing
      local token
      token=$(_get_token_for_api)
      local api_base
      api_base=$(_get_api_base_for_raw)

      curl -s -X PUT \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/json" \
        -d "{\"due_on\":\"$yesterday\"}" \
        "$api_base/buckets/$project_id/todos/$existing_id.json" >/dev/null

      bcq --json reopen "$existing_id" --project "$project_id" >/dev/null 2>&1 || true
      overdue_ids+=("$existing_id")
      log "Updated in P2: $content ($existing_id) due=$yesterday"
    fi
  done

  # Store comma-separated list of IDs
  fixtures[overdue_ids_2]=$(IFS=,; echo "${overdue_ids[*]}")
  log "Stored overdue IDs (P2): ${fixtures[overdue_ids_2]}"
}

# Get a person ID for assignment tests
get_person_id() {
  log "Getting person ID for assignment tests..."

  # Get the current user's ID
  local person_id
  person_id=$(bcq --json me | jq -r '.data.id')

  if [[ -z "$person_id" ]] || [[ "$person_id" == "null" ]]; then
    die "Failed to get person ID"
  fi

  log "Person ID: $person_id"
  fixtures[person_id]="$person_id"
}

# Write fixtures to JSON file
write_fixtures() {
  log "Writing fixtures to $FIXTURES_FILE..."

  local json="{"
  local first=true
  for key in "${!fixtures[@]}"; do
    if [[ "$first" == "true" ]]; then
      first=false
    else
      json+=","
    fi
    json+="\"$key\":\"${fixtures[$key]}\""
  done
  json+="}"

  echo "$json" | jq '.' > "$FIXTURES_FILE"
  log "Fixtures written:"
  cat "$FIXTURES_FILE" >&2
}

# Main
main() {
  log "Starting benchmark fixture seeding..."

  # Project 1 (primary benchmark project)
  find_or_create_project
  get_todoset
  get_messageboard
  find_or_create_todolist
  get_person_id
  seed_todos
  create_named_todos
  create_overdue_todos
  create_seeded_message
  create_malicious_message

  # Project 2 (for Task 12 cross-project tests - also 75 todos to force pagination)
  find_or_create_project_2
  get_todoset_2
  find_or_create_todolist_2
  seed_todos_project_2
  create_overdue_todos_project_2

  write_fixtures

  log "Seeding complete!"
}

main "$@"
