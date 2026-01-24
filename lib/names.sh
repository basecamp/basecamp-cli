#!/usr/bin/env bash
# names.sh - Name resolution for bcq
#
# Allows using human-readable names instead of IDs for projects, people, and todolists.
# Uses a session cache to avoid repeated API calls.


# Cache directory (session-scoped temp files)
_BCQ_NAMES_CACHE_DIR="${TMPDIR:-/tmp}/bcq-names-$$"

# Clean up cache on shell exit to prevent disk leak
# The trap is only set if not already set (allows nested sourcing)
if [[ -z "${_BCQ_NAMES_TRAP_SET:-}" ]]; then
  _BCQ_NAMES_TRAP_SET=1
  trap '_names_clear_cache 2>/dev/null' EXIT
fi

# Global error message for resolution failures
# Initialized here so it's always declared (resolver functions run in subshells
# via command substitution, so their assignments don't reach the parent shell)
RESOLVE_ERROR=""


# ============================================================================
# Cache Management
# ============================================================================

_names_ensure_cache_dir() {
  if [[ ! -d "$_BCQ_NAMES_CACHE_DIR" ]]; then
    mkdir -p "$_BCQ_NAMES_CACHE_DIR"
  fi
}

_names_get_cache() {
  local type="$1"
  local file="$_BCQ_NAMES_CACHE_DIR/${type}.json"
  if [[ -f "$file" ]]; then
    cat "$file"
  fi
}

_names_set_cache() {
  local type="$1"
  local data="$2"
  _names_ensure_cache_dir
  echo "$data" > "$_BCQ_NAMES_CACHE_DIR/${type}.json"
}

_names_clear_cache() {
  rm -rf "$_BCQ_NAMES_CACHE_DIR"
}


# ============================================================================
# Project Resolution
# ============================================================================

# Resolve a project name or ID to an ID
# Args: $1 - project name, partial name, or ID
# Returns: project ID (or empty if not found)
# Sets: RESOLVE_ERROR with error message if ambiguous/not found
resolve_project_id() {
  local input="$1"
  RESOLVE_ERROR=""

  # If it's numeric, assume it's an ID
  if [[ "$input" =~ ^[0-9]+$ ]]; then
    echo "$input"
    return 0
  fi

  # Fetch projects (with cache)
  local projects
  projects=$(_names_get_cache "projects")
  if [[ -z "$projects" ]]; then
    projects=$(api_get_all "/projects.json") || return 1
    _names_set_cache "projects" "$projects"
  fi

  # Try exact match first
  local exact_match
  exact_match=$(echo "$projects" | jq -r --arg name "$input" \
    '.[] | select(.name == $name) | .id' | head -1)
  if [[ -n "$exact_match" ]]; then
    echo "$exact_match"
    return 0
  fi

  # Try case-insensitive match
  local ci_matches
  ci_matches=$(echo "$projects" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase == ($name | ascii_downcase)) | .id')
  local ci_count=0
  [[ -n "$ci_matches" ]] && ci_count=$(echo "$ci_matches" | grep -c . || true)
  if [[ "$ci_count" -eq 1 ]]; then
    echo "$ci_matches"
    return 0
  fi

  # Try partial match (contains)
  local partial_matches
  partial_matches=$(echo "$projects" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase | contains($name | ascii_downcase)) | "\(.id):\(.name)"')
  local partial_count=0
  [[ -n "$partial_matches" ]] && partial_count=$(echo "$partial_matches" | grep -c . || true)

  if [[ "$partial_count" -eq 1 ]]; then
    echo "$partial_matches" | cut -d: -f1
    return 0
  elif [[ "$partial_count" -gt 1 ]]; then
    RESOLVE_ERROR="Ambiguous project name '$input' ($partial_count matches):"
    while IFS=: read -r id name; do
      RESOLVE_ERROR+=$'\n'"  - $name ($id)"
    done <<< "$partial_matches"
    return 1
  fi

  # Not found
  RESOLVE_ERROR="Project not found: $input"
  local suggestions
  suggestions=$(_names_suggest_similar "$input" "$projects" "name")
  if [[ -n "$suggestions" ]]; then
    RESOLVE_ERROR+=$'\n'"Did you mean: $suggestions?"
  fi
  return 1
}

# Get cached projects list
get_projects_list() {
  local projects
  projects=$(_names_get_cache "projects")
  if [[ -z "$projects" ]]; then
    projects=$(api_get_all "/projects.json") || return 1
    _names_set_cache "projects" "$projects"
  fi
  echo "$projects"
}


# ============================================================================
# Person Resolution
# ============================================================================

# Resolve a person name, email, or ID to an ID
# Args: $1 - person name, email, "me", partial name, or ID
# Returns: person ID (or empty if not found)
# Sets: RESOLVE_ERROR with error message if ambiguous/not found
resolve_person_id() {
  local input="$1"
  RESOLVE_ERROR=""

  # Handle "me" shortcut
  if [[ "$input" == "me" ]]; then
    local profile
    profile=$(api_get "/my/profile.json") || return 1
    echo "$profile" | jq -r '.id'
    return 0
  fi

  # If it's numeric, assume it's an ID
  if [[ "$input" =~ ^[0-9]+$ ]]; then
    echo "$input"
    return 0
  fi

  # Fetch people (with cache)
  local people
  people=$(_names_get_cache "people")
  if [[ -z "$people" ]]; then
    people=$(api_get_all "/people.json") || return 1
    _names_set_cache "people" "$people"
  fi

  # Try exact email match first
  if [[ "$input" == *@* ]]; then
    local email_match
    email_match=$(echo "$people" | jq -r --arg email "$input" \
      '.[] | select(.email_address == $email) | .id' | head -1)
    if [[ -n "$email_match" ]]; then
      echo "$email_match"
      return 0
    fi
  fi

  # Try exact name match
  local exact_match
  exact_match=$(echo "$people" | jq -r --arg name "$input" \
    '.[] | select(.name == $name) | .id' | head -1)
  if [[ -n "$exact_match" ]]; then
    echo "$exact_match"
    return 0
  fi

  # Try case-insensitive name match
  local ci_matches
  ci_matches=$(echo "$people" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase == ($name | ascii_downcase)) | .id')
  local ci_count=0
  [[ -n "$ci_matches" ]] && ci_count=$(echo "$ci_matches" | grep -c . || true)
  if [[ "$ci_count" -eq 1 ]]; then
    echo "$ci_matches"
    return 0
  fi

  # Try partial name match (contains)
  local partial_matches
  partial_matches=$(echo "$people" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase | contains($name | ascii_downcase)) | "\(.id):\(.name)"')
  local partial_count=0
  [[ -n "$partial_matches" ]] && partial_count=$(echo "$partial_matches" | grep -c . || true)

  if [[ "$partial_count" -eq 1 ]]; then
    echo "$partial_matches" | cut -d: -f1
    return 0
  elif [[ "$partial_count" -gt 1 ]]; then
    RESOLVE_ERROR="Ambiguous person name '$input' ($partial_count matches):"
    while IFS=: read -r id name; do
      RESOLVE_ERROR+=$'\n'"  - $name ($id)"
    done <<< "$partial_matches"
    return 1
  fi

  # Not found
  RESOLVE_ERROR="Person not found: $input"
  local suggestions
  suggestions=$(_names_suggest_similar "$input" "$people" "name")
  if [[ -n "$suggestions" ]]; then
    RESOLVE_ERROR+=$'\n'"Did you mean: $suggestions?"
  fi
  return 1
}

# Get cached people list
get_people_list() {
  local people
  people=$(_names_get_cache "people")
  if [[ -z "$people" ]]; then
    people=$(api_get_all "/people.json") || return 1
    _names_set_cache "people" "$people"
  fi
  echo "$people"
}


# ============================================================================
# Todolist Resolution
# ============================================================================

# Resolve a todolist name or ID to an ID (within a project)
# Args: $1 - todolist name, partial name, or ID
#       $2 - project ID (required)
# Returns: todolist ID (or empty if not found)
# Sets: RESOLVE_ERROR with error message if ambiguous/not found
resolve_todolist_id() {
  local input="$1"
  local project_id="$2"
  RESOLVE_ERROR=""

  if [[ -z "$project_id" ]]; then
    RESOLVE_ERROR="Project ID required for todolist resolution"
    return 1
  fi

  # If it's numeric, assume it's an ID
  if [[ "$input" =~ ^[0-9]+$ ]]; then
    echo "$input"
    return 0
  fi

  # First get the todoset for this project
  local todoset_id
  todoset_id=$(_get_todoset_id "$project_id") || return 1

  # Fetch todolists (with cache per project)
  local cache_key="todolists_${project_id}"
  local todolists
  todolists=$(_names_get_cache "$cache_key")
  if [[ -z "$todolists" ]]; then
    todolists=$(api_get_all "/buckets/$project_id/todosets/$todoset_id/todolists.json") || return 1
    _names_set_cache "$cache_key" "$todolists"
  fi

  # Try exact match first
  local exact_match
  exact_match=$(echo "$todolists" | jq -r --arg name "$input" \
    '.[] | select(.name == $name) | .id' | head -1)
  if [[ -n "$exact_match" ]]; then
    echo "$exact_match"
    return 0
  fi

  # Try case-insensitive match
  local ci_matches
  ci_matches=$(echo "$todolists" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase == ($name | ascii_downcase)) | .id')
  local ci_count=0
  [[ -n "$ci_matches" ]] && ci_count=$(echo "$ci_matches" | grep -c . || true)
  if [[ "$ci_count" -eq 1 ]]; then
    echo "$ci_matches"
    return 0
  fi

  # Try partial match (contains)
  local partial_matches
  partial_matches=$(echo "$todolists" | jq -r --arg name "$input" \
    '.[] | select(.name | ascii_downcase | contains($name | ascii_downcase)) | "\(.id):\(.name)"')
  local partial_count=0
  [[ -n "$partial_matches" ]] && partial_count=$(echo "$partial_matches" | grep -c . || true)

  if [[ "$partial_count" -eq 1 ]]; then
    echo "$partial_matches" | cut -d: -f1
    return 0
  elif [[ "$partial_count" -gt 1 ]]; then
    RESOLVE_ERROR="Ambiguous todolist name '$input' ($partial_count matches):"
    while IFS=: read -r id name; do
      RESOLVE_ERROR+=$'\n'"  - $name ($id)"
    done <<< "$partial_matches"
    return 1
  fi

  # Not found
  RESOLVE_ERROR="Todolist not found: $input"
  local suggestions
  suggestions=$(_names_suggest_similar "$input" "$todolists" "name")
  if [[ -n "$suggestions" ]]; then
    RESOLVE_ERROR+=$'\n'"Did you mean: $suggestions?"
  fi
  return 1
}

# Helper: Get todoset ID for a project (cached)
_get_todoset_id() {
  local project_id="$1"
  local cache_key="todoset_${project_id}"

  local todoset_id
  todoset_id=$(_names_get_cache "$cache_key")
  if [[ -n "$todoset_id" ]]; then
    echo "$todoset_id"
    return 0
  fi

  # Fetch project to get dock with todoset
  local project
  project=$(api_get "/projects/$project_id.json") || return 1
  todoset_id=$(echo "$project" | jq -r '.dock[] | select(.name == "todoset") | .id')

  if [[ -z "$todoset_id" ]] || [[ "$todoset_id" == "null" ]]; then
    RESOLVE_ERROR="Project has no todoset"
    return 1
  fi

  _names_set_cache "$cache_key" "$todoset_id"
  echo "$todoset_id"
}


# ============================================================================
# Suggestion Helpers
# ============================================================================

# Suggest similar names using simple substring matching
# Args: $1 - input string
#       $2 - JSON array of objects
#       $3 - field name to match against
# Returns: comma-separated list of similar names (up to 3)
_names_suggest_similar() {
  local input="$1"
  local json_array="$2"
  local field="$3"

  # Get all names
  local names
  names=$(echo "$json_array" | jq -r ".[].$field // empty")

  [[ -z "$names" ]] && return 0

  # Find names that share a common prefix (first 3 chars)
  local prefix="${input:0:3}"
  local suggestions=""

  if [[ ${#prefix} -ge 2 ]]; then
    suggestions=$(echo "$names" | grep -iF "$prefix" 2>/dev/null | head -3 | tr '\n' ', ' | sed 's/, $//')
  fi

  # If no prefix matches, try substring matches
  if [[ -z "$suggestions" ]] && [[ ${#input} -ge 2 ]]; then
    suggestions=$(echo "$names" | grep -iF "$input" 2>/dev/null | head -3 | tr '\n' ', ' | sed 's/, $//')
  fi

  # If still nothing, return first few available options
  if [[ -z "$suggestions" ]]; then
    suggestions=$(echo "$names" | head -3 | tr '\n' ', ' | sed 's/, $//')
  fi

  echo "$suggestions"
}


# ============================================================================
# Error Formatting
# ============================================================================

# Format resolution error for die() with hint
# Args: $1 - entity type (project, person, todolist)
#       $2 - the failed input
# Uses: RESOLVE_ERROR
format_resolve_error() {
  local type="$1"
  local input="$2"

  if [[ -n "$RESOLVE_ERROR" ]]; then
    echo "$RESOLVE_ERROR"
  else
    # Capitalize first letter (Bash 3.2 compatible)
    local capitalized
    capitalized="$(echo "${type:0:1}" | tr '[:lower:]' '[:upper:]')${type:1}"
    echo "$capitalized not found: $input"
  fi
}


# ============================================================================
# Integration Helpers
# ============================================================================

# Require a project ID (from argument or config), resolving names if needed
# Args: $1 - optional project (name/ID from flag, or empty to use config)
#       $2 - optional custom hint for error message (default: "Use --in <project> or set in .basecamp/config.json")
# Returns: resolved numeric project ID
# Dies: if project not specified and not in config, or if resolution fails
require_project_id() {
  local input="${1:-}"
  local hint="${2:-Use --in <project> or set in .basecamp/config.json}"
  local project

  # Use input if provided, otherwise get from config
  if [[ -n "$input" ]]; then
    project="$input"
  else
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified. $hint" $EXIT_USAGE
  fi

  # Resolve name to ID (resolve_project_id handles numeric IDs efficiently)
  local resolved
  resolved=$(resolve_project_id "$project") || die "$(format_resolve_error project "$project")" $EXIT_NOT_FOUND
  echo "$resolved"
}

# Require a person ID (from argument), resolving names if needed
# Args: $1 - person name, email, or ID
# Returns: resolved numeric person ID
# Dies: if resolution fails
require_person_id() {
  local input="$1"

  if [[ -z "$input" ]]; then
    die "Person identifier required" $EXIT_USAGE
  fi

  local resolved
  resolved=$(resolve_person_id "$input") || die "$(format_resolve_error person "$input")" $EXIT_NOT_FOUND
  echo "$resolved"
}

# Require a todolist ID (from argument or config), resolving names if needed
# Args: $1 - optional todolist (name/ID from flag, or empty to use config)
#       $2 - project ID (required for resolution)
# Returns: resolved numeric todolist ID
# Dies: if not specified/found, or if resolution fails
require_todolist_id() {
  local input="${1:-}"
  local project_id="$2"
  local todolist

  # Use input if provided, otherwise get from config
  if [[ -n "$input" ]]; then
    todolist="$input"
  else
    todolist=$(get_todolist_id)
  fi

  if [[ -z "$todolist" ]]; then
    die "No todolist specified. Use --list <todolist> or set todolist_id in config" $EXIT_USAGE
  fi

  # Resolve name to ID
  local resolved
  resolved=$(resolve_todolist_id "$todolist" "$project_id") || die "$(format_resolve_error todolist "$todolist")" $EXIT_NOT_FOUND
  echo "$resolved"
}
