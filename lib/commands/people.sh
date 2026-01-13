#!/usr/bin/env bash
# people.sh - People/user management commands


cmd_people() {
  local action="${1:-list}"
  shift || true

  case "$action" in
    list|"")
      _people_list "$@"
      ;;
    show|get)
      _people_show "$@"
      ;;
    pingable)
      _people_pingable "$@"
      ;;
    --help|-h)
      _help_people
      ;;
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
    response=$(api_get "/buckets/$project_id/people.json")
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


# Resolve assignee to person ID
# Accepts: "me" or numeric ID
# Returns: numeric ID or empty string on error
resolve_assignee() {
  local assignee="$1"

  if [[ "$assignee" == "me" ]]; then
    local profile
    profile=$(api_get "/my/profile.json")
    echo "$profile" | jq -r '.id'
  elif [[ "$assignee" =~ ^[0-9]+$ ]]; then
    echo "$assignee"
  else
    # Invalid format - return empty to signal error
    echo ""
  fi
}
