#!/usr/bin/env bash
# url.sh - Basecamp URL parsing

cmd_url() {
  local action="${1:-}"

  case "$action" in
    parse) shift; _url_parse "$@" ;;
    --help|-h) _help_url ;;
    *)
      if [[ -n "$action" ]] && [[ "$action" != -* ]]; then
        # Assume it's a URL to parse
        _url_parse "$action" "$@"
      else
        _help_url
      fi
      ;;
  esac
}

_url_parse() {
  local url=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_url_parse
        return
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$url" ]]; then
          url="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$url" ]]; then
    die "URL required" $EXIT_USAGE "Usage: bcq url parse <url>"
  fi

  # Validate it's a Basecamp URL
  if [[ ! "$url" =~ basecamp\.com ]]; then
    die "Not a Basecamp URL: $url" $EXIT_USAGE "Expected URL like: https://3.basecamp.com/..."
  fi

  # Parse the URL
  # Pattern: https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}[#{fragment}]
  # Or: https://3.basecamp.com/{account}/projects/{project}[/{section}]

  local account_id="" bucket_id="" recording_type="" recording_id="" comment_id=""
  local url_path fragment=""

  # Extract fragment if present
  if [[ "$url" =~ \#(.+)$ ]]; then
    fragment="${BASH_REMATCH[1]}"
    url_path="${url%%#*}"
  else
    url_path="$url"
  fi

  # Remove protocol and domain
  local path_only
  path_only=$(echo "$url_path" | sed -E 's|^https?://[^/]+||')

  # Parse path components
  # Expected formats:
  #   /{account}/buckets/{bucket}/{type}/{id}
  #   /{account}/buckets/{bucket}/card_tables/cards/{id}  (nested card path)
  #   /{account}/projects/{project}
  #   /{account}/buckets/{bucket}/{type}  (list view)

  if [[ "$path_only" =~ ^/([0-9]+)/buckets/([0-9]+)/card_tables/cards/([0-9]+) ]]; then
    # Card URL: /{account}/buckets/{bucket}/card_tables/cards/{id}
    account_id="${BASH_REMATCH[1]}"
    bucket_id="${BASH_REMATCH[2]}"
    recording_type="cards"
    recording_id="${BASH_REMATCH[3]}"
  elif [[ "$path_only" =~ ^/([0-9]+)/buckets/([0-9]+)/card_tables/columns/([0-9]+) ]]; then
    # Column URL: /{account}/buckets/{bucket}/card_tables/columns/{id}
    account_id="${BASH_REMATCH[1]}"
    bucket_id="${BASH_REMATCH[2]}"
    recording_type="columns"
    recording_id="${BASH_REMATCH[3]}"
  elif [[ "$path_only" =~ ^/([0-9]+)/buckets/([0-9]+)/([^/]+)/([0-9]+) ]]; then
    # Full recording URL: /{account}/buckets/{bucket}/{type}/{id}
    account_id="${BASH_REMATCH[1]}"
    bucket_id="${BASH_REMATCH[2]}"
    recording_type="${BASH_REMATCH[3]}"
    recording_id="${BASH_REMATCH[4]}"
  elif [[ "$path_only" =~ ^/([0-9]+)/buckets/([0-9]+)/([^/]+)/?$ ]]; then
    # Type list URL: /{account}/buckets/{bucket}/{type}
    account_id="${BASH_REMATCH[1]}"
    bucket_id="${BASH_REMATCH[2]}"
    recording_type="${BASH_REMATCH[3]}"
  elif [[ "$path_only" =~ ^/([0-9]+)/projects/([0-9]+) ]]; then
    # Project URL: /{account}/projects/{project}
    account_id="${BASH_REMATCH[1]}"
    bucket_id="${BASH_REMATCH[2]}"
    recording_type="project"
  elif [[ "$path_only" =~ ^/([0-9]+) ]]; then
    # Account-only URL
    account_id="${BASH_REMATCH[1]}"
  else
    die "Could not parse URL path: $path_only" $EXIT_USAGE
  fi

  # Parse fragment for comment ID
  # Fragment format: __recording_{id} or just {id}
  if [[ -n "$fragment" ]]; then
    if [[ "$fragment" =~ __recording_([0-9]+) ]]; then
      comment_id="${BASH_REMATCH[1]}"
    elif [[ "$fragment" =~ ^[0-9]+$ ]]; then
      comment_id="$fragment"
    fi
  fi

  # Normalize recording type (remove trailing 's' for singular form)
  local type_singular="$recording_type"
  case "$recording_type" in
    messages) type_singular="message" ;;
    todos) type_singular="todo" ;;
    todolists) type_singular="todolist" ;;
    documents) type_singular="document" ;;
    comments) type_singular="comment" ;;
    uploads) type_singular="upload" ;;
    cards) type_singular="card" ;;
    columns) type_singular="column" ;;
    chats|campfires) type_singular="campfire" ;;
    schedules) type_singular="schedule" ;;
    schedule_entries) type_singular="schedule_entry" ;;
    vaults) type_singular="vault" ;;
  esac

  # Capitalize first letter (Bash 3.2 compatible)
  local type_capitalized
  type_capitalized="$(echo "${type_singular:0:1}" | tr '[:lower:]' '[:upper:]')${type_singular:1}"

  # Build summary
  local summary=""
  if [[ -n "$recording_id" ]]; then
    summary="$type_capitalized #$recording_id"
    if [[ -n "$bucket_id" ]]; then
      summary+=" in project #$bucket_id"
    fi
    if [[ -n "$comment_id" ]]; then
      summary+=", comment #$comment_id"
    fi
  elif [[ -n "$bucket_id" ]]; then
    if [[ "$recording_type" == "project" ]]; then
      summary="Project #$bucket_id"
    else
      summary="$type_capitalized list in project #$bucket_id"
    fi
  elif [[ -n "$account_id" ]]; then
    summary="Account #$account_id"
  else
    summary="Basecamp URL"
  fi

  # Build result JSON
  local result
  result=$(jq -n \
    --arg url "$url" \
    --arg account_id "$account_id" \
    --arg bucket_id "$bucket_id" \
    --arg type "$recording_type" \
    --arg type_singular "$type_singular" \
    --arg recording_id "$recording_id" \
    --arg comment_id "$comment_id" \
    '{
      url: $url,
      account_id: (if $account_id == "" then null else $account_id end),
      bucket_id: (if $bucket_id == "" then null else $bucket_id end),
      type: (if $type == "" then null else $type end),
      type_singular: (if $type_singular == "" then null else $type_singular end),
      recording_id: (if $recording_id == "" then null else $recording_id end),
      comment_id: (if $comment_id == "" then null else $comment_id end)
    }')

  # Build breadcrumbs based on what we parsed
  local bcs="[]"

  if [[ -n "$recording_id" ]] && [[ -n "$bucket_id" ]]; then
    if [[ "$type_singular" == "column" ]]; then
      # Column URL - show card operations for this column
      bcs=$(breadcrumbs \
        "$(breadcrumb "cards" "bcq cards --in $bucket_id --column $recording_id" "List cards in column")" \
        "$(breadcrumb "create" "bcq card --title \"Title\" --in $bucket_id --column $recording_id" "Create card in column")" \
        "$(breadcrumb "columns" "bcq cards columns --in $bucket_id" "List all columns")")
    else
      bcs=$(breadcrumbs \
        "$(breadcrumb "show" "bcq show $type_singular $recording_id --project $bucket_id" "View the $type_singular")" \
        "$(breadcrumb "comment" "bcq comment \"text\" --on $recording_id --project $bucket_id" "Add a comment")" \
        "$(breadcrumb "comments" "bcq comments --on $recording_id --project $bucket_id" "List comments")")

      if [[ -n "$comment_id" ]]; then
        # Append comment breadcrumb to existing
        local comment_bc
        comment_bc=$(breadcrumb "show-comment" "bcq comments show $comment_id --project $bucket_id" "View the comment")
        bcs=$(echo "$bcs" | jq --argjson bc "$comment_bc" '. + [$bc]')
      fi
    fi
  fi

  output "$result" "$summary" "$bcs" "_url_parse_md"
}

_url_parse_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Parsed URL"
  echo
  echo "**$summary**"
  echo

  local account_id bucket_id type recording_id comment_id
  account_id=$(echo "$data" | jq -r '.account_id // ""')
  bucket_id=$(echo "$data" | jq -r '.bucket_id // ""')
  type=$(echo "$data" | jq -r '.type // ""')
  recording_id=$(echo "$data" | jq -r '.recording_id // ""')
  comment_id=$(echo "$data" | jq -r '.comment_id // ""')

  echo "| Component | Value |"
  echo "|-----------|-------|"
  [[ -n "$account_id" ]] && echo "| Account | $account_id |"
  [[ -n "$bucket_id" ]] && echo "| Project | $bucket_id |"
  [[ -n "$type" ]] && echo "| Type | $type |"
  [[ -n "$recording_id" ]] && echo "| Recording ID | $recording_id |"
  [[ -n "$comment_id" ]] && echo "| Comment ID | $comment_id |"
  echo

  md_breadcrumbs "$breadcrumbs"
}

_help_url() {
  cat <<'EOF'
## bcq url

Parse and work with Basecamp URLs.

### Usage

    bcq url parse <url>     Parse a Basecamp URL
    bcq url <url>           Shorthand for parse

### Examples

    # Parse a message URL
    bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982"

    # Parse URL with comment fragment
    bcq url "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598"

    # Get JSON output
    bcq url parse "https://..." --json

EOF
}

_help_url_parse() {
  cat <<'EOF'
## bcq url parse

Parse a Basecamp URL into its components.

### Usage

    bcq url parse <url>

### URL Formats

Supported Basecamp URL patterns:

    https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}
    https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}#__recording_{comment}
    https://3.basecamp.com/{account}/projects/{project}

### Output

Returns JSON with:
- account_id: Basecamp account number
- bucket_id: Project/bucket ID
- type: Recording type (messages, todos, etc.)
- recording_id: The recording's ID
- comment_id: Comment ID from URL fragment (if present)

### Examples

    # Parse message URL
    bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982"

    # With comment fragment
    bcq url parse "https://3.basecamp.com/2914079/buckets/41746046/messages/9478142982#__recording_9488783598"

### Common Workflows

Reply to a comment (comments are flat, so you comment on the parent recording):

    bcq url parse "https://..." --json | jq -r '.data | "bcq comment \"reply\" --on \(.recording_id) --in \(.bucket_id)"'

EOF
}
