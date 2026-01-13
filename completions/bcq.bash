# bcq bash completion
# Source this file or place in /etc/bash_completion.d/

_bcq_completions() {
  local cur prev words cword
  _init_completion || return

  local commands="
    auth config help version self-update
    projects todos todolists todolistgroups todosets
    todo done reopen assign unassign
    messages message messageboards messagetypes
    campfire comments comment
    cards card files docs uploads vaults
    people me search recordings show
    webhooks templates schedule events
    timesheet checkins forwards subscriptions lineup
    mcp
  "

  local auth_subcommands="login logout status"
  local config_subcommands="init set unset project"
  local todos_subcommands="list show create complete uncomplete position"
  local todolists_subcommands="list show create update"
  local todolistgroups_subcommands="list show create update position"
  local todosets_subcommands="show"
  local messages_subcommands="list show create update pin unpin"
  local campfire_subcommands="list messages post line delete"
  local cards_subcommands="list show create update move columns steps"
  local files_subcommands="list show docs documents doc document uploads upload vaults vault folders folder update"
  local recordings_subcommands="list trash archive restore visibility todos messages documents comments cards uploads"
  local webhooks_subcommands="list show create update delete"
  local lineup_subcommands="create update delete"

  case "$prev" in
    bcq)
      COMPREPLY=($(compgen -W "$commands" -- "$cur"))
      return
      ;;
    auth)
      COMPREPLY=($(compgen -W "$auth_subcommands" -- "$cur"))
      return
      ;;
    config)
      COMPREPLY=($(compgen -W "$config_subcommands" -- "$cur"))
      return
      ;;
    todos)
      COMPREPLY=($(compgen -W "$todos_subcommands" -- "$cur"))
      return
      ;;
    todolists)
      COMPREPLY=($(compgen -W "$todolists_subcommands" -- "$cur"))
      return
      ;;
    todolistgroups)
      COMPREPLY=($(compgen -W "$todolistgroups_subcommands" -- "$cur"))
      return
      ;;
    todosets)
      COMPREPLY=($(compgen -W "$todosets_subcommands" -- "$cur"))
      return
      ;;
    messages)
      COMPREPLY=($(compgen -W "$messages_subcommands" -- "$cur"))
      return
      ;;
    campfire)
      COMPREPLY=($(compgen -W "$campfire_subcommands" -- "$cur"))
      return
      ;;
    cards)
      COMPREPLY=($(compgen -W "$cards_subcommands" -- "$cur"))
      return
      ;;
    files|docs|uploads|vaults)
      COMPREPLY=($(compgen -W "$files_subcommands" -- "$cur"))
      return
      ;;
    recordings)
      COMPREPLY=($(compgen -W "$recordings_subcommands" -- "$cur"))
      return
      ;;
    webhooks)
      COMPREPLY=($(compgen -W "$webhooks_subcommands" -- "$cur"))
      return
      ;;
    lineup)
      COMPREPLY=($(compgen -W "$lineup_subcommands" -- "$cur"))
      return
      ;;
    --project|--in|-p)
      # Could complete project IDs if cached, for now just return
      return
      ;;
    --status|-s)
      COMPREPLY=($(compgen -W "active archived trashed completed" -- "$cur"))
      return
      ;;
  esac

  # Handle flags
  if [[ "$cur" == -* ]]; then
    local flags="--json -j --md -m --markdown --quiet -q --data --verbose -v --project -p --account -a --cache-dir --help -h"
    COMPREPLY=($(compgen -W "$flags" -- "$cur"))
    return
  fi
}

complete -F _bcq_completions bcq
