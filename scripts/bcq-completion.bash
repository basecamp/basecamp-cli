#!/usr/bin/env bash
# bcq-completion.bash - Tab completion for bcq CLI
#
# Installation:
#   source /path/to/bcq-completion.bash
#
# Or add to your shell config:
#   echo 'source /path/to/bcq-completion.bash' >> ~/.bashrc
#   echo 'source /path/to/bcq-completion.bash' >> ~/.zshrc

_bcq_completions() {
  local cur prev words cword
  _init_completion 2>/dev/null || {
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    words=("${COMP_WORDS[@]}")
    cword=$COMP_CWORD
  }

  # Top-level commands
  local commands="projects todos todolists todo done comment campfire cards card people recordings search show assign unassign me auth config help"

  # Subcommands by resource
  local projects_actions="list show"
  local todos_actions="list show create complete"
  local todolists_actions="list show"
  local campfire_actions="list messages post"
  local cards_actions="list show create move"
  local people_actions="list show pingable"
  local recordings_actions="list todos messages documents comments cards uploads"
  local auth_actions="login logout status"
  local config_actions="show init set unset project"

  # Global flags
  local global_flags="--json --md --quiet --verbose --project --account --help"

  # Common flags
  local project_flags="--in --project -p"
  local assignee_flags="--assignee --to -a"
  local due_flags="--due -d"

  case "${words[1]}" in
    projects)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$projects_actions" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "--status $global_flags" -- "$cur"))
      fi
      ;;
    todos)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$todos_actions $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$project_flags --list --assignee --status $global_flags" -- "$cur"))
      fi
      ;;
    todolists)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$todolists_actions $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$project_flags $global_flags" -- "$cur"))
      fi
      ;;
    todo)
      COMPREPLY=($(compgen -W "$project_flags --list $assignee_flags $due_flags $global_flags" -- "$cur"))
      ;;
    done)
      COMPREPLY=($(compgen -W "--project $global_flags" -- "$cur"))
      ;;
    comment)
      COMPREPLY=($(compgen -W "--on --project $global_flags" -- "$cur"))
      ;;
    campfire)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$campfire_actions $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$project_flags --campfire --limit $global_flags" -- "$cur"))
      fi
      ;;
    cards)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$cards_actions $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$project_flags --column --to $global_flags" -- "$cur"))
      fi
      ;;
    card)
      COMPREPLY=($(compgen -W "$project_flags --column $global_flags" -- "$cur"))
      ;;
    show)
      COMPREPLY=($(compgen -W "todo todolist message comment card document $project_flags --type $global_flags" -- "$cur"))
      ;;
    assign|unassign)
      COMPREPLY=($(compgen -W "--to $project_flags $global_flags" -- "$cur"))
      ;;
    me)
      COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
      ;;
    people)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$people_actions $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$project_flags $global_flags" -- "$cur"))
      fi
      ;;
    recordings)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "todos messages documents comments cards uploads --type $project_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "--type $project_flags --status --sort --direction --limit $global_flags" -- "$cur"))
      fi
      ;;
    search)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "metadata types --type --project --creator --limit $global_flags" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "--type --project --creator --limit $global_flags" -- "$cur"))
      fi
      ;;
    auth)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$auth_actions" -- "$cur"))
      else
        case "${words[2]}" in
          login)
            COMPREPLY=($(compgen -W "--scope --no-browser $global_flags" -- "$cur"))
            ;;
          *)
            COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
            ;;
        esac
      fi
      ;;
    config)
      if [[ $cword -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$config_actions" -- "$cur"))
      else
        case "${words[2]}" in
          set)
            if [[ $cword -eq 3 ]]; then
              COMPREPLY=($(compgen -W "account_id project_id todolist_id" -- "$cur"))
            else
              COMPREPLY=($(compgen -W "--global --local" -- "$cur"))
            fi
            ;;
          *)
            COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
            ;;
        esac
      fi
      ;;
    --*)
      # Global flag at position 1
      COMPREPLY=($(compgen -W "$commands $global_flags" -- "$cur"))
      ;;
    *)
      # Default: show commands
      if [[ $cword -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$commands $global_flags" -- "$cur"))
      fi
      ;;
  esac
}

# Zsh compatibility - must come before complete
if [[ -n "$ZSH_VERSION" ]]; then
  autoload -U +X bashcompinit && bashcompinit
  autoload -U +X compinit && compinit
fi

# Register completion
complete -F _bcq_completions bcq
