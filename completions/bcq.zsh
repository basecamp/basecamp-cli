#compdef bcq
# bcq zsh completion
# Place in a directory in your $fpath (e.g., ~/.zsh/completions/)

_bcq() {
  local -a commands
  commands=(
    'auth:Authentication (login, logout, status)'
    'config:Configuration management'
    'help:Show help'
    'version:Show version'
    'projects:List and manage projects'
    'todos:List and manage todos'
    'todolists:List and manage todolists'
    'todolistgroups:Manage todolist groups'
    'todosets:Todoset container'
    'todo:Create a todo (shorthand)'
    'done:Complete a todo (shorthand)'
    'reopen:Reopen a completed todo'
    'assign:Assign a todo'
    'unassign:Unassign a todo'
    'messages:List and manage messages'
    'message:Create a message (shorthand)'
    'messageboards:Message board containers'
    'messagetypes:Message type categories'
    'campfire:Campfire chat'
    'comments:List comments'
    'comment:Add a comment (shorthand)'
    'cards:Kanban cards'
    'card:Create a card (shorthand)'
    'files:Files, docs, uploads'
    'docs:Documents'
    'uploads:Uploaded files'
    'vaults:Folders (vaults)'
    'people:List people'
    'me:Current user info'
    'search:Search across projects'
    'recordings:Browse recordings by type'
    'show:Show any resource by ID'
    'webhooks:Manage webhooks'
    'templates:Project templates'
    'schedule:Schedule entries'
    'events:Event audit trail'
    'timesheet:Time tracking'
    'checkins:Automatic check-ins'
    'forwards:Email forwards'
    'subscriptions:Notification subscriptions'
    'lineup:Account-wide lineup markers'
    'mcp:MCP server mode'
  )

  local -a auth_commands
  auth_commands=(
    'login:Authenticate via OAuth'
    'logout:Clear credentials'
    'status:Show auth status'
  )

  local -a config_commands
  config_commands=(
    'init:Create config interactively'
    'set:Set a config value'
    'unset:Remove a config value'
    'project:Select default project'
  )

  local -a recordings_commands
  recordings_commands=(
    'list:List recordings by type'
    'trash:Trash a recording'
    'archive:Archive a recording'
    'restore:Restore a recording'
    'visibility:Set client visibility'
    'todos:List todos across projects'
    'messages:List messages across projects'
    'documents:List documents across projects'
    'comments:List comments across projects'
    'cards:List cards across projects'
    'uploads:List uploads across projects'
  )

  local -a lineup_commands
  lineup_commands=(
    'create:Create a lineup marker'
    'update:Update a lineup marker'
    'delete:Delete a lineup marker'
  )

  _arguments -C \
    '(-j --json)'{-j,--json}'[Force JSON output]' \
    '(-m --md --markdown)'{-m,--md,--markdown}'[Force Markdown output]' \
    '(-q --quiet --data)'{-q,--quiet,--data}'[Minimal output]' \
    '(-v --verbose)'{-v,--verbose}'[Debug output]' \
    '(-p --project)'{-p,--project}'[Override project]:project:' \
    '(-a --account)'{-a,--account}'[Override account]:account:' \
    '--cache-dir[Cache directory]:path:' \
    '(-h --help)'{-h,--help}'[Show help]' \
    '1: :->command' \
    '*::arg:->args'

  case "$state" in
    command)
      _describe -t commands 'bcq command' commands
      ;;
    args)
      case "$words[1]" in
        auth)
          _describe -t auth_commands 'auth subcommand' auth_commands
          ;;
        config)
          _describe -t config_commands 'config subcommand' config_commands
          ;;
        recordings)
          _describe -t recordings_commands 'recordings subcommand' recordings_commands
          ;;
        lineup)
          _describe -t lineup_commands 'lineup subcommand' lineup_commands
          ;;
      esac
      ;;
  esac
}

_bcq "$@"
