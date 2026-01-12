#!/usr/bin/env bash
# me.sh - Current user info

cmd_me() {
  local response
  response=$(api_get "/my/profile.json")

  local name email_address
  name=$(echo "$response" | jq -r '.name')
  email_address=$(echo "$response" | jq -r '.email_address')
  local summary="$name <$email_address>"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "projects" "bcq projects" "List your projects")" \
    "$(breadcrumb "todos" "bcq todos --assignee me" "Your assigned todos")" \
    "$(breadcrumb "auth" "bcq auth status" "Auth status")"
  )

  output "$response" "$summary" "$bcs" "_me_md"
}

_me_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local id name email_address admin owner time_zone
  id=$(echo "$data" | jq -r '.id')
  name=$(echo "$data" | jq -r '.name')
  email_address=$(echo "$data" | jq -r '.email_address')
  admin=$(echo "$data" | jq -r '.admin')
  owner=$(echo "$data" | jq -r '.owner')
  time_zone=$(echo "$data" | jq -r '.time_zone // "Not set"')

  local avatar_url
  avatar_url=$(echo "$data" | jq -r '.avatar_url // empty')

  echo "## $name"
  echo
  md_kv "Email" "$email_address" \
        "ID" "$id" \
        "Time Zone" "$time_zone"

  # Show role if admin or owner
  if [[ "$admin" == "true" ]] || [[ "$owner" == "true" ]]; then
    local roles=""
    [[ "$owner" == "true" ]] && roles="Owner"
    [[ "$admin" == "true" ]] && roles="${roles:+$roles, }Admin"
    echo "**Role:** $roles"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}
