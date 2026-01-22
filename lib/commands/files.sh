#!/usr/bin/env bash
# files.sh - Docs & Files commands (vaults, uploads, documents)

# Main entry point for files/docs
cmd_files() {
  local action="${1:-list}"

  if [[ "$action" == -* ]] || [[ -z "$action" ]]; then
    _files_list "$@"
    return
  fi

  shift || true

  case "$action" in
    doc|document) _documents_create "$@" ;;
    docs|documents) _documents_list "$@" ;;
    folder|vault) _vaults_create "$@" ;;
    folders|vaults) _vaults_list "$@" ;;
    list) _files_list "$@" ;;
    show) _files_show "$@" ;;
    update) _files_update "$@" ;;
    upload) _uploads_create "$@" ;;
    uploads) _uploads_list "$@" ;;
    --help|-h) _help_files ;;
    *)
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _files_show "$action" "$@"
      else
        die "Unknown files action: $action" $EXIT_USAGE "Run: bcq files --help"
      fi
      ;;
  esac
}

# Aliases - check for help first, then delegate
cmd_vaults() {
  if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "-h" ]]; then
    _help_files
    return
  fi
  cmd_files folders "$@"
}
cmd_uploads() {
  if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "-h" ]]; then
    _help_files
    return
  fi
  cmd_files uploads "$@"
}
cmd_docs() {
  if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "-h" ]]; then
    _help_files
    return
  fi
  cmd_files docs "$@"
}

# Get the root vault ID from project dock
_get_vault_id() {
  local project="$1"
  local project_data
  project_data=$(api_get "/projects/$project.json")
  echo "$project_data" | jq -r '.dock[] | select(.name == "vault") | .id // empty'
}

_files_list() {
  local project="" vault_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --help|-h)
        _help_files
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Get root vault if not specified
  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  if [[ -z "$vault_id" ]]; then
    die "No Docs & Files found in project $project" $EXIT_NOT_FOUND
  fi

  # Get vault details
  local vault_data
  vault_data=$(api_get "/buckets/$project/vaults/$vault_id.json")

  local vault_title
  vault_title=$(echo "$vault_data" | jq -r '.title // "Docs & Files"')

  # Get contents: subvaults, uploads, documents
  local vaults uploads documents
  vaults=$(api_get "/buckets/$project/vaults/$vault_id/vaults.json" 2>/dev/null || echo '[]')
  uploads=$(api_get "/buckets/$project/vaults/$vault_id/uploads.json" 2>/dev/null || echo '[]')
  documents=$(api_get "/buckets/$project/vaults/$vault_id/documents.json" 2>/dev/null || echo '[]')

  # Combine into single response
  local response
  response=$(jq -n \
    --argjson vaults "$vaults" \
    --argjson uploads "$uploads" \
    --argjson documents "$documents" \
    --arg vault_id "$vault_id" \
    --arg vault_title "$vault_title" \
    '{vault_id: ($vault_id | tonumber), vault_title: $vault_title, folders: $vaults, files: $uploads, documents: $documents}')

  local folder_count file_count doc_count
  folder_count=$(echo "$vaults" | jq 'length')
  file_count=$(echo "$uploads" | jq 'length')
  doc_count=$(echo "$documents" | jq 'length')
  local summary="$folder_count folders, $file_count files, $doc_count documents"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq files <id> --in $project" "Show item details")" \
    "$(breadcrumb "folder" "bcq files folder \"name\" --in $project" "Create folder")"
  )

  output "$response" "$summary" "$bcs" "_files_list_md"
}

_files_list_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local vault_title
  vault_title=$(echo "$data" | jq -r '.vault_title')

  echo "## $vault_title ($summary)"
  echo

  # Folders
  local folder_count
  folder_count=$(echo "$data" | jq '.folders | length')
  if [[ "$folder_count" -gt 0 ]]; then
    echo "### Folders"
    echo "| ID | Name | Items |"
    echo "|----|------|-------|"
    echo "$data" | jq -r '.folders[] | "| \(.id) | \(.title) | \(.vaults_count + .uploads_count + .documents_count) |"'
    echo
  fi

  # Documents
  local doc_count
  doc_count=$(echo "$data" | jq '.documents | length')
  if [[ "$doc_count" -gt 0 ]]; then
    echo "### Documents"
    echo "| ID | Title | Updated |"
    echo "|----|-------|---------|"
    echo "$data" | jq -r '.documents[] | "| \(.id) | \(.title | .[0:40]) | \(.updated_at | split("T")[0]) |"'
    echo
  fi

  # Files
  local file_count
  file_count=$(echo "$data" | jq '.files | length')
  if [[ "$file_count" -gt 0 ]]; then
    echo "### Files"
    echo "| ID | Filename | Size | Type |"
    echo "|----|----------|------|------|"
    echo "$data" | jq -r '.files[] | "| \(.id) | \(.filename | .[0:30]) | \(.byte_size | . / 1024 | floor)KB | \(.content_type | split("/")[1] // "-") |"'
    echo
  fi

  if [[ "$folder_count" -eq 0 ]] && [[ "$doc_count" -eq 0 ]] && [[ "$file_count" -eq 0 ]]; then
    echo "*Empty folder*"
    echo
  fi

  md_breadcrumbs "$breadcrumbs"
}

_vaults_list() {
  local project="" vault_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --help|-h)
        _help_files
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/vaults/$vault_id/vaults.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count folders"

  output "$response" "$summary" "" "_vaults_list_md"
}

_vaults_list_md() {
  local data="$1"
  local summary="$2"

  echo "## Folders ($summary)"
  echo
  local count
  count=$(echo "$data" | jq 'length')
  if [[ "$count" -eq 0 ]]; then
    echo "*No folders*"
  else
    echo "| ID | Name | Docs | Files | Subfolders |"
    echo "|----|------|------|-------|------------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title) | \(.documents_count) | \(.uploads_count) | \(.vaults_count) |"'
  fi
  echo
}

_uploads_list() {
  local project="" vault_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --help|-h)
        _help_files
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/vaults/$vault_id/uploads.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count files"

  output "$response" "$summary" "" "_uploads_list_md"
}

_uploads_list_md() {
  local data="$1"
  local summary="$2"

  echo "## Files ($summary)"
  echo
  local count
  count=$(echo "$data" | jq 'length')
  if [[ "$count" -eq 0 ]]; then
    echo "*No files*"
  else
    echo "| ID | Filename | Size | Type | Uploaded |"
    echo "|----|----------|------|------|----------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.filename | .[0:30]) | \(.byte_size | . / 1024 | floor)KB | \(.content_type | split("/")[1] // "-") | \(.created_at | split("T")[0]) |"'
  fi
  echo
}

_documents_list() {
  local project="" vault_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --help|-h)
        _help_files
        return
        ;;
      *)
        shift
        ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/vaults/$vault_id/documents.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count documents"

  output "$response" "$summary" "" "_documents_list_md"
}

_documents_list_md() {
  local data="$1"
  local summary="$2"

  echo "## Documents ($summary)"
  echo
  local count
  count=$(echo "$data" | jq 'length')
  if [[ "$count" -eq 0 ]]; then
    echo "*No documents*"
  else
    echo "| ID | Title | Author | Updated |"
    echo "|----|-------|--------|---------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title | .[0:40]) | \(.creator.name) | \(.updated_at | split("T")[0]) |"'
  fi
  echo
}

_files_show() {
  local item_id="${1:-}"
  shift || true
  local project="" type=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --type|-t)
        [[ -z "${2:-}" ]] && die "--type requires a value" $EXIT_USAGE
        type="$2"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  if [[ -z "$item_id" ]]; then
    die "Item ID required" $EXIT_USAGE
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Try to detect type if not specified
  local response
  if [[ -z "$type" ]]; then
    # Try vault first
    response=$(api_get "/buckets/$project/vaults/$item_id.json" 2>/dev/null || echo "")
    if [[ -n "$response" ]]; then
      type="vault"
    else
      # Try upload
      response=$(api_get "/buckets/$project/uploads/$item_id.json" 2>/dev/null || echo "")
      if [[ -n "$response" ]]; then
        type="upload"
      else
        # Try document
        response=$(api_get "/buckets/$project/documents/$item_id.json" 2>/dev/null || echo "")
        if [[ -n "$response" ]]; then
          type="document"
        fi
      fi
    fi
  else
    case "$type" in
      vault|folder) response=$(api_get "/buckets/$project/vaults/$item_id.json") ;;
      upload|file) response=$(api_get "/buckets/$project/uploads/$item_id.json") ;;
      document|doc) response=$(api_get "/buckets/$project/documents/$item_id.json") ;;
      *) die "Invalid type: $type (use vault, upload, or document)" $EXIT_USAGE ;;
    esac
  fi

  if [[ -z "$response" ]]; then
    die "Item $item_id not found" $EXIT_NOT_FOUND
  fi

  local item_type
  item_type=$(echo "$response" | jq -r '.type // "Unknown"')
  local title
  title=$(echo "$response" | jq -r '.title // .filename // "Untitled"')
  local summary="$item_type: $title"

  output "$response" "$summary" "" "_files_show_md"
}

_files_show_md() {
  local data="$1"
  local summary="$2"

  local item_type
  item_type=$(echo "$data" | jq -r '.type')

  case "$item_type" in
    Vault)
      local id title
      id=$(echo "$data" | jq -r '.id')
      title=$(echo "$data" | jq -r '.title')
      echo "## Folder: $title (#$id)"
      echo
      md_kv "Documents" "$(echo "$data" | jq -r '.documents_count')" \
            "Files" "$(echo "$data" | jq -r '.uploads_count')" \
            "Subfolders" "$(echo "$data" | jq -r '.vaults_count')"
      ;;
    Upload)
      local id filename size
      id=$(echo "$data" | jq -r '.id')
      filename=$(echo "$data" | jq -r '.filename')
      size=$(echo "$data" | jq -r '.byte_size | . / 1024 | floor')
      echo "## File: $filename (#$id)"
      echo
      md_kv "Type" "$(echo "$data" | jq -r '.content_type')" \
            "Size" "${size}KB" \
            "Folder" "$(echo "$data" | jq -r '.parent.title // "-"')"
      local download_url
      download_url=$(echo "$data" | jq -r '.download_url // empty')
      if [[ -n "$download_url" ]]; then
        echo "**Download:** $download_url"
        echo
      fi
      ;;
    Document)
      local id title content
      id=$(echo "$data" | jq -r '.id')
      title=$(echo "$data" | jq -r '.title')
      echo "## Document: $title (#$id)"
      echo
      md_kv "Author" "$(echo "$data" | jq -r '.creator.name')" \
            "Updated" "$(echo "$data" | jq -r '.updated_at | split("T")[0]')" \
            "Folder" "$(echo "$data" | jq -r '.parent.title // "-"')"
      content=$(echo "$data" | jq -r '.content // empty')
      if [[ -n "$content" ]]; then
        echo "### Content"
        echo "$content" | sed 's/<[^>]*>//g' | head -20
        echo
      fi
      ;;
    *)
      echo "## $summary"
      echo
      echo "$data" | jq '.'
      ;;
  esac
}

_uploads_create() {
  local file="" project="" vault_id="" description="" base_name=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --description|--desc|-d)
        [[ -z "${2:-}" ]] && die "--description requires a value" $EXIT_USAGE
        description="$2"
        shift 2
        ;;
      --name|-n)
        [[ -z "${2:-}" ]] && die "--name requires a value" $EXIT_USAGE
        base_name="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$file" ]]; then
    die "File path required" $EXIT_USAGE "Usage: bcq files upload <file> --in <project>"
  fi

  if [[ ! -f "$file" ]]; then
    die "File not found: $file" $EXIT_NOT_FOUND
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  if [[ -z "$vault_id" ]]; then
    die "No Docs & Files found in project" $EXIT_NOT_FOUND
  fi

  # Get filename and content type
  local filename content_type file_size
  filename=$(basename "$file")
  content_type=$(file --mime-type -b "$file" 2>/dev/null || echo "application/octet-stream")
  file_size=$(wc -c < "$file" | tr -d ' ')

  # Step 1: Upload attachment to get attachable_sgid
  local attach_response attachable_sgid
  attach_response=$(api_upload "/attachments.json?name=$(urlencode "$filename")" "$file" "$content_type")
  attachable_sgid=$(echo "$attach_response" | jq -r '.attachable_sgid // empty')

  if [[ -z "$attachable_sgid" ]]; then
    die "Failed to upload attachment" $EXIT_API "No attachable_sgid returned"
  fi

  # Step 2: Create upload in vault
  local payload
  payload=$(jq -n --arg sgid "$attachable_sgid" '{attachable_sgid: $sgid}')

  if [[ -n "$description" ]]; then
    payload=$(echo "$payload" | jq --arg d "$description" '. + {description: $d}')
  fi

  if [[ -n "$base_name" ]]; then
    payload=$(echo "$payload" | jq --arg n "$base_name" '. + {base_name: $n}')
  fi

  local response
  response=$(api_post "/buckets/$project/vaults/$vault_id/uploads.json" "$payload")

  local upload_id
  upload_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Uploaded #$upload_id: $filename"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq files $upload_id --in $project" "View upload")" \
    "$(breadcrumb "list" "bcq files uploads --in $project" "List uploads")"
  )

  output "$response" "$summary" "$bcs"
}


_vaults_create() {
  local title="" project="" vault_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      -*)
        shift
        ;;
      *)
        if [[ -z "$title" ]]; then
          title="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$title" ]]; then
    die "Folder name required" $EXIT_USAGE "Usage: bcq files folder \"name\" --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  local response
  response=$(api_post "/buckets/$project/vaults/$vault_id/vaults.json" "$payload")

  local new_id
  new_id=$(echo "$response" | jq -r '.id')
  local summary="Created folder #$new_id: $title"

  output "$response" "$summary" ""
}


_help_doc_create() {
  cat << 'EOF'
bcq files doc - Create a new document

USAGE
  bcq files doc --title "title" [options]

OPTIONS
  --title, -n <text>        Document title (required)
  --in, --project, -p <id>  Project ID or name
  --vault, --folder <id>    Parent folder ID (default: root)
  --content, --body, -b     Document body content

EXAMPLES
  bcq files doc --title "Meeting Notes" --in 123
  bcq files doc --title "Spec" --content "## Overview" --in "My Project"
EOF
}

_documents_create() {
  local title="" project="" vault_id="" content=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        _help_doc_create
        return
        ;;
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --vault|--folder)
        [[ -z "${2:-}" ]] && die "--vault requires a value" $EXIT_USAGE
        vault_id="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      --title|-n)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      -*)
        die "Unknown option: $1" $EXIT_USAGE "Run: bcq files doc --help"
        ;;
      *)
        die "Unexpected argument: $1" $EXIT_USAGE "Run: bcq files doc --help"
        ;;
    esac
  done

  if [[ -z "$title" ]]; then
    die "Document title required" $EXIT_USAGE "Usage: bcq files doc --title \"title\" --in <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$vault_id" ]]; then
    vault_id=$(_get_vault_id "$project")
  fi

  if [[ -z "$vault_id" ]]; then
    die "No Docs & Files found in project" $EXIT_NOT_FOUND
  fi

  local payload
  payload=$(jq -n --arg title "$title" '{title: $title}')

  if [[ -n "$content" ]]; then
    payload=$(echo "$payload" | jq --arg c "$content" '. + {content: $c}')
  fi

  local response
  response=$(api_post "/buckets/$project/vaults/$vault_id/documents.json" "$payload")

  local doc_id
  doc_id=$(echo "$response" | jq -r '.id')
  local summary="✓ Created document #$doc_id: $title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq files $doc_id --in $project" "View document")" \
    "$(breadcrumb "list" "bcq files docs --in $project" "List documents")"
  )

  output "$response" "$summary" "$bcs"
}


_files_update() {
  local item_id="" project="" title="" content="" type=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --type|-t)
        [[ -z "${2:-}" ]] && die "--type requires a value" $EXIT_USAGE
        type="$2"
        shift 2
        ;;
      --title|--name|-n)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$item_id" ]]; then
          item_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$item_id" ]]; then
    die "Item ID required" $EXIT_USAGE "Usage: bcq files update <id> --title \"new title\" --in <project>"
  fi

  if [[ -z "$title" ]] && [[ -z "$content" ]]; then
    die "Title or content required" $EXIT_USAGE "Use --title and/or --content"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  # Auto-detect type if not specified
  local endpoint=""
  if [[ -n "$type" ]]; then
    case "$type" in
      vault|folder) endpoint="/buckets/$project/vaults/$item_id.json" ;;
      document|doc) endpoint="/buckets/$project/documents/$item_id.json" ;;
      upload|file) endpoint="/buckets/$project/uploads/$item_id.json" ;;
      *) die "Invalid type: $type (use vault, document, or upload)" $EXIT_USAGE ;;
    esac
  else
    # Try document first (most common update case)
    if api_get "/buckets/$project/documents/$item_id.json" &>/dev/null; then
      endpoint="/buckets/$project/documents/$item_id.json"
      type="document"
    elif api_get "/buckets/$project/vaults/$item_id.json" &>/dev/null; then
      endpoint="/buckets/$project/vaults/$item_id.json"
      type="vault"
    elif api_get "/buckets/$project/uploads/$item_id.json" &>/dev/null; then
      endpoint="/buckets/$project/uploads/$item_id.json"
      type="upload"
    else
      die "Item $item_id not found" $EXIT_NOT_FOUND "Specify --type if needed"
    fi
  fi

  local payload="{}"
  [[ -n "$title" ]] && payload=$(echo "$payload" | jq --arg t "$title" '. + {title: $t}')
  [[ -n "$content" ]] && payload=$(echo "$payload" | jq --arg c "$content" '. + {content: $c}')

  local response
  response=$(api_put "$endpoint" "$payload")

  local summary="Updated $type #$item_id"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "show" "bcq files $item_id --in $project" "View item")"
  )

  output "$response" "$summary" "$bcs"
}


_help_files() {
  cat <<'EOF'
## bcq files

Manage Docs & Files (vaults, uploads, documents).

### Usage

    bcq files [action] [options]

### Actions

    doc "title"       Create a new document
    docs              List only documents
    folder "name"     Create a new folder
    folders           List only folders (vaults)
    list              List all items in folder (default)
    show <id>         Show item details
    update <id>       Update document/vault/upload
    upload <file>     Upload a file
    uploads           List only uploaded files

### Options

    --in, -p <project>      Project ID
    --vault, --folder       Parent folder ID (default: root)
    --type <type>           Item type (vault, document, upload)
    --title, -n <title>     New title (for update)
    --content, -b <text>    Content/body (for create/update)
    --description, -d       Description (for upload)
    --name                  Base name without extension (for upload)

### Examples

    # List root Docs & Files
    bcq files --in 12345

    # List specific folder
    bcq files --in 12345 --folder 67890

    # List only documents
    bcq files docs --in 12345

    # Create document
    bcq files doc --title "Meeting Notes" --in 12345
    bcq files doc --title "Spec" --content "## Overview" --in 12345

    # Upload a file
    bcq files upload report.pdf --in 12345
    bcq files upload logo.png --description "New logo" --in 12345

    # Update document/vault/upload
    bcq files update 11111 --title "New Title" --in 12345
    bcq files update 11111 --content "Updated content" --in 12345

    # Create folder
    bcq files folder "Project Assets" --in 12345

    # Show file details
    bcq files show 11111 --in 12345

EOF
}
