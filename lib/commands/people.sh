#!/usr/bin/env bash
# people.sh - People/user management commands


cmd_people() {
  local action="${1:-list}"
  shift || true

  case "$action" in
    add) _people_add "$@" ;;
    list|"") _people_list "$@" ;;
    pingable) _people_pingable "$@" ;;
    remove) _people_remove "$@" ;;
    show) _people_show "$@" ;;
    --help|-h) _help_people ;;
    *)
      # If it looks like an ID, show that person
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _people_show "$action" "$@"
      else
        die "Unknown people action: $action" $EXIT_USAGE "Run: bcq people --help"
      fi
      ;;
  esac
}


_people_list() {
  local project_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project|--in|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project_id="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  local response
  if [[ -n "$project_id" ]]; then
    response=$(api_get "/projects/$project_id/people.json")
  else
    response=$(api_get "/people.json")
  fi

  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    local summary
    local count
    count=$(echo "$response" | jq 'length')
    summary="$count people"

    local bcs
    bcs=$(breadcrumbs \
      "$(breadcrumb "show" "bcq people <id>" "Show person details")"
    )

    json_ok "$response" "$summary" "$bcs"
  else
    echo "## People"
    echo
    echo "$response" | jq -r '.[] | "- **\(.name)** (\(.email_address // "no email")) #\(.id)"'
  fi
}


_people_show() {
  local person_id="$1"

  if [[ -z "$person_id" ]]; then
    die "Person ID required" $EXIT_USAGE "Usage: bcq people show <id>"
  fi

  local response
  response=$(api_get "/people/$person_id.json")

  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    local name
    name=$(echo "$response" | jq -r '.name')
    json_ok "$response" "$name"
  else
    local name email title company
    name=$(echo "$response" | jq -r '.name')
    email=$(echo "$response" | jq -r '.email_address // "—"')
    title=$(echo "$response" | jq -r '.title // "—"')
    company=$(echo "$response" | jq -r '.company.name // "—"')

    echo "## $name"
    echo
    echo "- **Email**: $email"
    echo "- **Title**: $title"
    echo "- **Company**: $company"
    echo "- **ID**: $(echo "$response" | jq -r '.id')"
  fi
}


_people_pingable() {
  local response
  response=$(api_get "/circles/people.json")

  local format
  format=$(get_format)

  if [[ "$format" == "json" ]]; then
    local count
    count=$(echo "$response" | jq 'length')
    json_ok "$response" "$count pingable people"
  else
    echo "## Pingable People"
    echo
    echo "$response" | jq -r '.[] | "- **\(.name)** #\(.id)"'
  fi
}


_people_add() {
  local project_id="" person_ids=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project_id="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]]; then
          person_ids+=("$1")
        fi
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project_id=$(require_project_id "${project_id:-}")

  if [[ ${#person_ids[@]} -eq 0 ]]; then
    die "Person ID(s) required" $EXIT_USAGE "Usage: bcq people add <id> [id...] --in <project>"
  fi

  # Build JSON array of IDs
  local ids_json
  ids_json=$(printf '%s\n' "${person_ids[@]}" | jq -R . | jq -s '{"grant": map(tonumber)}')

  local response
  response=$(api_put "/projects/$project_id/people/users.json" "$ids_json")

  local count=${#person_ids[@]}
  local summary="Added $count person(s) to project #$project_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq people --in $project_id" "List project members")"
  )

  output "${response:-'{}'}" "$summary" "$bcs"
}

_people_remove() {
  local project_id="" person_ids=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project_id="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]]; then
          person_ids+=("$1")
        fi
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project_id=$(require_project_id "${project_id:-}")

  if [[ ${#person_ids[@]} -eq 0 ]]; then
    die "Person ID(s) required" $EXIT_USAGE "Usage: bcq people remove <id> [id...] --in <project>"
  fi

  # Build JSON array of IDs to revoke
  local ids_json
  ids_json=$(printf '%s\n' "${person_ids[@]}" | jq -R . | jq -s '{"revoke": map(tonumber)}')

  local response
  response=$(api_put "/projects/$project_id/people/users.json" "$ids_json")

  local count=${#person_ids[@]}
  local summary="Removed $count person(s) from project #$project_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "list" "bcq people --in $project_id" "List project members")"
  )

  output "${response:-'{}'}" "$summary" "$bcs"
}


# Resolve assignee to person ID
# Accepts: "me", name, email, or numeric ID
# Returns: numeric ID or empty string on error
# Note: Uses names.sh resolve_person_id for name/email resolution
resolve_assignee() {
  local assignee="$1"

  # Use resolve_person_id which handles "me", names, emails, and numeric IDs
  local resolved
  resolved=$(resolve_person_id "$assignee" 2>/dev/null)
  if [[ -n "$resolved" ]]; then
    echo "$resolved"
  else
    # Return empty to signal error (caller handles error message)
    echo ""
  fi
}
